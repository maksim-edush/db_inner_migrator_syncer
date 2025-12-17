package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type MySQLAdapter struct {
	db *sql.DB
}

func (m *MySQLAdapter) Provider() string { return "mysql" }

func (m *MySQLAdapter) Close() error { return m.db.Close() }

func (m *MySQLAdapter) EnsureMigrationTable(ctx context.Context, table string) error {
	tableName := fmt.Sprintf("`%s`", strings.ReplaceAll(table, "`", "``"))
	stmt := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
	id bigint AUTO_INCREMENT PRIMARY KEY,
	migration_name varchar(255) NOT NULL,
	script_file varchar(255) NOT NULL,
	rollback_file varchar(255),
	status varchar(32) NOT NULL,
	applied_env varchar(32) NOT NULL,
	checksum varchar(128),
	applied_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
	error text,
	INDEX migration_status_name_env_idx (migration_name, applied_env)
) ENGINE=InnoDB;
`, tableName)
	_, err := m.db.ExecContext(ctx, stmt)
	return err
}

func (m *MySQLAdapter) InsertStatus(ctx context.Context, table string, entry MigrationEntry) error {
	tableName := fmt.Sprintf("`%s`", strings.ReplaceAll(table, "`", "``"))
	stmt := fmt.Sprintf(`INSERT INTO %s
		(migration_name, script_file, rollback_file, status, applied_env, checksum, applied_at, error)
		VALUES (?,?,?,?,?,?,?,?)`, tableName)
	_, err := m.db.ExecContext(ctx, stmt,
		entry.MigrationName,
		entry.ScriptFile,
		entry.RollbackFile,
		entry.Status,
		entry.AppliedEnv,
		entry.Checksum,
		entry.AppliedAt,
		nullString(entry.Error),
	)
	return err
}

func (m *MySQLAdapter) UpdateStatus(ctx context.Context, table string, entry MigrationEntry) error {
	tableName := fmt.Sprintf("`%s`", strings.ReplaceAll(table, "`", "``"))
	stmt := fmt.Sprintf(`
UPDATE %s SET status=?, applied_at=?, error=?, rollback_file=?
WHERE migration_name=? AND applied_env=?
ORDER BY applied_at DESC, id DESC
LIMIT 1
`, tableName)
	_, err := m.db.ExecContext(ctx, stmt,
		entry.Status,
		entry.AppliedAt,
		nullString(entry.Error),
		entry.RollbackFile,
		entry.MigrationName,
		entry.AppliedEnv,
	)
	return err
}

func (m *MySQLAdapter) FetchStatuses(ctx context.Context, table string, limit int) ([]MigrationEntry, error) {
	tableName := fmt.Sprintf("`%s`", strings.ReplaceAll(table, "`", "``"))
	stmt := fmt.Sprintf(`SELECT migration_name, script_file, rollback_file, status, applied_env, checksum, applied_at, error
FROM %s
ORDER BY applied_at DESC, id DESC
LIMIT ?`, tableName)
	rows, err := m.db.QueryContext(ctx, stmt, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MigrationEntry
	for rows.Next() {
		var e MigrationEntry
		if err := rows.Scan(&e.MigrationName, &e.ScriptFile, &e.RollbackFile, &e.Status, &e.AppliedEnv, &e.Checksum, &e.AppliedAt, &e.Error); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (m *MySQLAdapter) ExecScript(ctx context.Context, script string) error {
	statements := splitStatements(script)
	for _, stmt := range statements {
		if _, err := m.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (m *MySQLAdapter) FetchSchema(ctx context.Context, schema string) (Schema, error) {
	schemaName := strings.TrimSpace(schema)
	if schemaName == "" {
		if err := m.db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&schemaName); err != nil {
			return Schema{Tables: map[string]Table{}}, err
		}
	}
	result := Schema{Tables: map[string]Table{}}

	tablesRows, err := m.db.QueryContext(ctx, `
SELECT table_name
FROM information_schema.tables
WHERE table_schema=? AND table_type='BASE TABLE'`, schemaName)
	if err != nil {
		return result, err
	}
	defer tablesRows.Close()

	for tablesRows.Next() {
		var name string
		if err := tablesRows.Scan(&name); err != nil {
			return result, err
		}
		result.Tables[name] = Table{
			Name:       name,
			Columns:    map[string]Column{},
			PrimaryKey: []string{},
		}
	}
	if err := tablesRows.Err(); err != nil {
		return result, err
	}

	colsRows, err := m.db.QueryContext(ctx, `
SELECT table_name, column_name, column_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema=?`, schemaName)
	if err != nil {
		return result, err
	}
	defer colsRows.Close()

	for colsRows.Next() {
		var tbl, col, dataType, nullable string
		var def sql.NullString
		if err := colsRows.Scan(&tbl, &col, &dataType, &nullable, &def); err != nil {
			return result, err
		}
		t, ok := result.Tables[tbl]
		if !ok {
			continue
		}
		t.Columns[col] = Column{
			Name:         col,
			DataType:     dataType,
			IsNullable:   strings.EqualFold(nullable, "YES"),
			DefaultValue: def,
		}
		result.Tables[tbl] = t
	}
	if err := colsRows.Err(); err != nil {
		return result, err
	}

	pkRows, err := m.db.QueryContext(ctx, `
SELECT tc.table_name, kcu.column_name, kcu.ordinal_position
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
 ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
 AND tc.table_name = kcu.table_name
WHERE tc.table_schema=? AND tc.constraint_type='PRIMARY KEY'
ORDER BY kcu.ordinal_position`, schemaName)
	if err != nil {
		return result, err
	}
	defer pkRows.Close()

	for pkRows.Next() {
		var tbl, col string
		var pos int
		if err := pkRows.Scan(&tbl, &col, &pos); err != nil {
			return result, err
		}
		t, ok := result.Tables[tbl]
		if !ok {
			continue
		}
		t.PrimaryKey = append(t.PrimaryKey, col)
		result.Tables[tbl] = t
	}
	return result, pkRows.Err()
}
