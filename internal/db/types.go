package db

import (
	"database/sql"
	"time"
)

// Schema holds the introspected structure of a database.
type Schema struct {
	Tables map[string]Table
}

// Table describes a table and its columns.
type Table struct {
	Name       string
	Columns    map[string]Column
	PrimaryKey []string
}

// Column describes a table column.
type Column struct {
	Name         string
	DataType     string
	IsNullable   bool
	DefaultValue sql.NullString
}

// MigrationEntry represents a migration status row stored in the database.
type MigrationEntry struct {
	MigrationName string
	ScriptFile    string
	RollbackFile  string
	Status        string
	AppliedEnv    string
	Checksum      string
	AppliedAt     time.Time
	Error         sql.NullString
}
