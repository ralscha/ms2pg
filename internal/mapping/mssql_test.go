package mapping

import (
	"testing"

	"pgloader-go/internal/catalog"
)

func TestApplyMapsCommonTypesAndDefaults(t *testing.T) {
	table := &catalog.Table{
		Schema: "dbo",
		Name:   "users",
		Columns: []*catalog.Column{
			{Name: "id", SourceType: "int", Identity: true},
			{Name: "created_at", SourceType: "datetime2", Default: "(getdate())"},
			{Name: "external_id", SourceType: "uniqueidentifier", Default: "(newid())"},
		},
	}

	if err := Apply(table); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if got := table.Columns[0].TargetType; got != "bigint" {
		t.Fatalf("identity target type = %q, want bigint", got)
	}
	if got := table.Columns[1].TargetType; got != "timestamp" {
		t.Fatalf("datetime2 target type = %q, want timestamp", got)
	}
	if got := table.Columns[1].Default; got != "CURRENT_TIMESTAMP" {
		t.Fatalf("datetime2 default = %q, want CURRENT_TIMESTAMP", got)
	}
	if got := table.Columns[2].Default; got != "gen_random_uuid()" {
		t.Fatalf("uuid default = %q, want gen_random_uuid()", got)
	}
}
