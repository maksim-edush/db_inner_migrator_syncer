package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type PostgresAdapter struct {
	db *sql.DB
}

func (p *PostgresAdapter) Provider() string { return "postgres" }

func (p *PostgresAdapter) Close() error { return p.db.Close() }

func (p *PostgresAdapter) EnsureMigrationTable(ctx context.Context, table string) error {
	tableName := quoteIdent(table)
	indexName := quoteIdent(fmt.Sprintf("%s_name_env_idx", table))
	stmt := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
	id bigserial PRIMARY KEY,
	migration_name varchar(255) NOT NULL,
	script_file varchar(255) NOT NULL,
	rollback_file varchar(255),
	status varchar(32) NOT NULL,
	applied_env varchar(32) NOT NULL,
	checksum varchar(128),
	applied_at timestamptz NOT NULL,
	error text
);
CREATE INDEX IF NOT EXISTS %s ON %s(migration_name, applied_env);
`, tableName, indexName, tableName)
	_, err := p.db.ExecContext(ctx, stmt)
	return err
}

func (p *PostgresAdapter) InsertStatus(ctx context.Context, table string, entry MigrationEntry) error {
	tableName := quoteIdent(table)
	stmt := fmt.Sprintf(`INSERT INTO %s
		(migration_name, script_file, rollback_file, status, applied_env, checksum, applied_at, error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, tableName)
	_, err := p.db.ExecContext(ctx, stmt,
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

func (p *PostgresAdapter) UpdateStatus(ctx context.Context, table string, entry MigrationEntry) error {
	tableName := quoteIdent(table)
	stmt := fmt.Sprintf(`
UPDATE %s SET status=$1, applied_at=$2, error=$3, rollback_file=$4
WHERE id = (
	SELECT id FROM %s WHERE migration_name=$5 AND applied_env=$6
	ORDER BY applied_at DESC, id DESC
	LIMIT 1
)
`, tableName, tableName)
	_, err := p.db.ExecContext(ctx, stmt,
		entry.Status,
		entry.AppliedAt,
		nullString(entry.Error),
		entry.RollbackFile,
		entry.MigrationName,
		entry.AppliedEnv,
	)
	return err
}

func (p *PostgresAdapter) FetchStatuses(ctx context.Context, table string, limit int) ([]MigrationEntry, error) {
	tableName := quoteIdent(table)
	stmt := fmt.Sprintf(`SELECT migration_name, script_file, rollback_file, status, applied_env, checksum, applied_at, error
FROM %s
ORDER BY applied_at DESC, id DESC
LIMIT $1`, tableName)
	rows, err := p.db.QueryContext(ctx, stmt, limit)
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

func (p *PostgresAdapter) ExecScript(ctx context.Context, script string) error {
	statements := splitStatements(script)
	for _, stmt := range statements {
		if _, err := p.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (p *PostgresAdapter) FetchSchema(ctx context.Context, schema string) (Schema, error) {
	if schema == "" {
		schema = "public"
	}
	result := Schema{Tables: map[string]Table{}}

	tablesRows, err := p.db.QueryContext(ctx, `
SELECT table_name
FROM information_schema.tables
WHERE table_schema=$1 AND table_type='BASE TABLE'`, schema)
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

	colsRows, err := p.db.QueryContext(ctx, `
SELECT table_name, column_name, data_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema=$1`, schema)
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

	pkRows, err := p.db.QueryContext(ctx, `
SELECT tc.table_name, kcu.column_name, kcu.ordinal_position
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
 AND tc.table_name = kcu.table_name
WHERE tc.table_schema=$1 AND tc.constraint_type='PRIMARY KEY'
ORDER BY kcu.ordinal_position`, schema)
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

func nullString(s sql.NullString) any {
	if s.Valid {
		return s.String
	}
	return nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// splitStatements is a small helper used by both providers to avoid driver
// differences around multi-statements.
func splitStatements(sqlText string) []string {
	var (
		out      []string
		current  strings.Builder
		inSingle bool
		inDouble bool
	)

	flush := func() {
		stmt := strings.TrimSpace(current.String())
		if stmt != "" {
			out = append(out, stmt)
		}
		current.Reset()
	}

	for _, r := range sqlText {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ';':
			if !inSingle && !inDouble {
				flush()
				continue
			}
		}
		current.WriteRune(r)
	}
	flush()
	return out
}
