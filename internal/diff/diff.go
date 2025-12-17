package diff

import (
	"fmt"
	"sort"
	"strings"

	"db_inner_migrator_syncer/internal/db"
)

// SchemaDiff describes differences between two schemas.
type SchemaDiff struct {
	OnlyInA []string
	OnlyInB []string
	Tables  map[string]TableDiff
}

// TableDiff captures per-table differences.
type TableDiff struct {
	OnlyInA        []string
	OnlyInB        []string
	Changed        []ColumnChange
	PrimaryKeyA    []string
	PrimaryKeyB    []string
	PrimaryKeyDiff bool
}

// ColumnChange marks a column present in both but with different attributes.
type ColumnChange struct {
	Name string
	A    db.Column
	B    db.Column
}

// Compare builds a diff between schema a (staging) and schema b (production).
func Compare(a, b db.Schema) SchemaDiff {
	res := SchemaDiff{
		Tables: map[string]TableDiff{},
	}

	aTables := sortedKeys(a.Tables)
	bTables := sortedKeys(b.Tables)

	res.OnlyInA = difference(aTables, bTables)
	res.OnlyInB = difference(bTables, aTables)

	for name, tableA := range a.Tables {
		tableB, ok := b.Tables[name]
		if !ok {
			continue
		}
		td := TableDiff{}
		td.PrimaryKeyA = append([]string{}, tableA.PrimaryKey...)
		td.PrimaryKeyB = append([]string{}, tableB.PrimaryKey...)
		if !equalStringSlices(tableA.PrimaryKey, tableB.PrimaryKey) {
			td.PrimaryKeyDiff = true
		}

		colsA := sortedKeys(tableA.Columns)
		colsB := sortedKeys(tableB.Columns)
		td.OnlyInA = difference(colsA, colsB)
		td.OnlyInB = difference(colsB, colsA)

		for colName, colA := range tableA.Columns {
			colB, ok := tableB.Columns[colName]
			if !ok {
				continue
			}
			if !columnsEqual(colA, colB) {
				td.Changed = append(td.Changed, ColumnChange{Name: colName, A: colA, B: colB})
			}
		}
		if td.PrimaryKeyDiff || len(td.OnlyInA) > 0 || len(td.OnlyInB) > 0 || len(td.Changed) > 0 {
			res.Tables[name] = td
		}
	}
	return res
}

func columnsEqual(a, b db.Column) bool {
	return strings.EqualFold(a.DataType, b.DataType) &&
		a.IsNullable == b.IsNullable &&
		normalizeDefault(a.DefaultValue.String) == normalizeDefault(b.DefaultValue.String)
}

func normalizeDefault(val string) string {
	return strings.TrimSpace(val)
}

// Describe returns a human-readable summary of differences.
func Describe(d SchemaDiff) string {
	if !d.HasChanges() {
		return "schemas match"
	}

	var lines []string
	if len(d.OnlyInA) > 0 {
		lines = append(lines, fmt.Sprintf("Tables only in A: %s", strings.Join(d.OnlyInA, ", ")))
	}
	if len(d.OnlyInB) > 0 {
		lines = append(lines, fmt.Sprintf("Tables only in B: %s", strings.Join(d.OnlyInB, ", ")))
	}

	tableNames := make([]string, 0, len(d.Tables))
	for name := range d.Tables {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)

	for _, name := range tableNames {
		td := d.Tables[name]
		if len(td.OnlyInA) > 0 {
			lines = append(lines, fmt.Sprintf("Table %s: columns only in A: %s", name, strings.Join(td.OnlyInA, ", ")))
		}
		if len(td.OnlyInB) > 0 {
			lines = append(lines, fmt.Sprintf("Table %s: columns only in B: %s", name, strings.Join(td.OnlyInB, ", ")))
		}
		for _, ch := range td.Changed {
			lines = append(lines, fmt.Sprintf("Table %s column %s differs (A: %s NULL:%v DEFAULT:%s | B: %s NULL:%v DEFAULT:%s)",
				name,
				ch.Name,
				ch.A.DataType, ch.A.IsNullable, normalizeDefault(ch.A.DefaultValue.String),
				ch.B.DataType, ch.B.IsNullable, normalizeDefault(ch.B.DefaultValue.String)))
		}
		if td.PrimaryKeyDiff {
			lines = append(lines, fmt.Sprintf("Table %s primary key differs (A: %v | B: %v)", name, td.PrimaryKeyA, td.PrimaryKeyB))
		}
	}
	return strings.Join(lines, "\n")
}

// HasChanges reports whether the diff contains meaningful differences.
func (d SchemaDiff) HasChanges() bool {
	return len(d.OnlyInA) > 0 || len(d.OnlyInB) > 0 || len(d.Tables) > 0
}

func sortedKeys[K comparable, V any](m map[K]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, fmt.Sprintf("%v", k))
	}
	sort.Strings(keys)
	return keys
}

func difference(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, v := range b {
		set[v] = struct{}{}
	}
	var out []string
	for _, v := range a {
		if _, ok := set[v]; !ok {
			out = append(out, v)
		}
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
