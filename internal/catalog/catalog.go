package catalog

import (
	"path"
	"slices"
	"strings"
)

type Database struct {
	Schemas []*Schema
}

type Filters struct {
	IncludeSchemas []string
	IncludeTables  []string
	ExcludeSchemas []string
	ExcludeTables  []string
}

type Schema struct {
	Name   string
	Tables []*Table
	Views  []*View
}

type Table struct {
	Schema      string
	Name        string
	Columns     []*Column
	PrimaryKey  []string
	Indexes     []*Index
	ForeignKeys []*ForeignKey
}

type Index struct {
	Name            string
	Columns         []string
	IncludedColumns []string
	Predicate       string
	Unique          bool
}

type ForeignKey struct {
	Name              string
	Columns           []string
	ReferencedSchema  string
	ReferencedTable   string
	ReferencedColumns []string
	UpdateRule        string
	DeleteRule        string
}

type Column struct {
	Name              string
	Ordinal           int
	SourceType        string
	TargetType        string
	Nullable          bool
	Default           string
	Identity          bool
	IdentitySeed      int64
	IdentityIncrement int64
	Length            int64
	Precision         int64
	Scale             int64
}

type View struct {
	Schema     string
	Name       string
	Definition string
}

func (db *Database) SortedSchemas() []*Schema {
	schemas := slices.Clone(db.Schemas)
	slices.SortFunc(schemas, func(left, right *Schema) int {
		return compare(left.Name, right.Name)
	})
	return schemas
}

func (schema *Schema) SortedTables() []*Table {
	tables := slices.Clone(schema.Tables)
	slices.SortFunc(tables, func(left, right *Table) int {
		return compare(left.Name, right.Name)
	})
	return tables
}

func (schema *Schema) SortedViews() []*View {
	views := slices.Clone(schema.Views)
	slices.SortFunc(views, func(left, right *View) int {
		return compare(left.Name, right.Name)
	})
	return views
}

func compare(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func (filters Filters) MatchesSchema(schema string) bool {
	candidate := strings.ToLower(schema)
	if len(filters.IncludeSchemas) != 0 {
		matched := false
		for _, pattern := range filters.IncludeSchemas {
			if matchPattern(pattern, candidate, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	for _, pattern := range filters.ExcludeSchemas {
		if matchPattern(pattern, candidate, candidate) {
			return false
		}
	}

	return true
}

func (filters Filters) MatchesTable(schema string, table string) bool {
	if !filters.MatchesSchema(schema) {
		return false
	}
	if len(filters.IncludeTables) == 0 {
		return !filters.matchesExcludedObject(schema, table)
	}
	return filters.matchesObject(schema, table) && !filters.matchesExcludedObject(schema, table)
}

func (filters Filters) MatchesView(schema string, view string) bool {
	if !filters.MatchesSchema(schema) {
		return false
	}
	if len(filters.IncludeTables) == 0 {
		return !filters.matchesExcludedObject(schema, view)
	}
	return filters.matchesObject(schema, view) && !filters.matchesExcludedObject(schema, view)
}

func (filters Filters) matchesObject(schema string, object string) bool {
	lowerSchema := strings.ToLower(schema)
	lowerObject := strings.ToLower(object)
	qualified := lowerSchema + "." + lowerObject

	for _, pattern := range filters.IncludeTables {
		if matchPattern(pattern, qualified, lowerObject) {
			return true
		}
	}

	return false
}

func (filters Filters) matchesExcludedObject(schema string, object string) bool {
	if len(filters.ExcludeTables) == 0 {
		return false
	}

	lowerSchema := strings.ToLower(schema)
	lowerObject := strings.ToLower(object)
	qualified := lowerSchema + "." + lowerObject

	for _, pattern := range filters.ExcludeTables {
		if matchPattern(pattern, qualified, lowerObject) {
			return true
		}
	}

	return false
}

func matchPattern(pattern string, qualified string, unqualified string) bool {
	lowerPattern := strings.ToLower(strings.TrimSpace(pattern))
	if lowerPattern == "" {
		return false
	}

	if strings.Contains(lowerPattern, ".") {
		matched, err := path.Match(lowerPattern, qualified)
		return err == nil && matched
	}

	matched, err := path.Match(lowerPattern, unqualified)
	return err == nil && matched
}
