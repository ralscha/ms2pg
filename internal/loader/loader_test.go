package loader

import (
	"context"
	"strings"
	"testing"

	"ms2pg/internal/catalog"
)

func TestRunnerLoggerDefaultsWhenNil(t *testing.T) {
	runner := Runner{}
	if runner.logger() == nil {
		t.Fatal("logger() returned nil, want default logger")
	}
}

func TestFilterForeignKeysDistinguishesDottedNames(t *testing.T) {
	reference := &catalog.ForeignKey{Name: "fk", ReferencedSchema: "a", ReferencedTable: "b.c"}
	table := &catalog.Table{Schema: "a.b", Name: "c", ForeignKeys: []*catalog.ForeignKey{reference}}
	database := &catalog.Database{Schemas: []*catalog.Schema{{Name: "a.b", Tables: []*catalog.Table{table}}}}
	filterForeignKeys(database)
	if len(table.ForeignKeys) != 0 {
		t.Fatal("kept foreign key to an excluded table with a colliding dotted name")
	}
}

func TestRunnerRejectsInvalidFiltersBeforeConnecting(t *testing.T) {
	runner := Runner{Config: Config{IncludeTables: []string{"[invalid"}}}
	err := runner.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "validate filters") {
		t.Fatalf("Run() error = %v, want filter validation error", err)
	}
}
