package mapping

import (
	"testing"

	"ms2pg/internal/catalog"
)

func TestApplyMapsCommonTypesAndDefaults(t *testing.T) {
	table := &catalog.Table{
		Schema: "dbo",
		Name:   "users",
		Columns: []*catalog.Column{
			{Name: "id", SourceType: "int", Identity: true},
			{Name: "created_at", SourceType: "datetime2", Default: "(getdate())"},
			{Name: "external_id", SourceType: "uniqueidentifier", Default: "(newid())"},
			{Name: "created_utc", SourceType: "datetime2", Default: "((GETUTCDATE()))"},
			{Name: "label", SourceType: "nvarchar", Default: "(N'unknown')"},
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
	if got := table.Columns[3].Default; got != "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')" {
		t.Fatalf("utc default = %q, want UTC timestamp expression", got)
	}
	if got := table.Columns[4].Default; got != "'unknown'" {
		t.Fatalf("unicode string default = %q, want stripped N prefix", got)
	}
}

func TestApplyMapsDefaultConstraintDefinitions(t *testing.T) {
	table := &catalog.Table{
		Schema: "dbo",
		Name:   "users",
		DefaultConstraints: []*catalog.DefaultConstraint{
			{Name: "df_users_created_at", Column: "created_at", Definition: "(getdate())"},
			{Name: "df_users_external_id", Column: "external_id", Definition: "(newid())"},
		},
	}

	if err := Apply(table); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if got := table.DefaultConstraints[0].Definition; got != "CURRENT_TIMESTAMP" {
		t.Fatalf("created_at default constraint = %q, want CURRENT_TIMESTAMP", got)
	}
	if got := table.DefaultConstraints[1].Definition; got != "gen_random_uuid()" {
		t.Fatalf("external_id default constraint = %q, want gen_random_uuid()", got)
	}
}

func TestMapDefaultPreservesNonWrappedExpressions(t *testing.T) {
	got := mapDefault("GETDATE()")
	if got != "CURRENT_TIMESTAMP" {
		t.Fatalf("mapDefault(GETDATE()) = %q, want CURRENT_TIMESTAMP", got)
	}

	got = mapDefault("SYSUTCDATETIME")
	if got != "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')" {
		t.Fatalf("mapDefault(SYSUTCDATETIME) = %q, want UTC timestamp expression", got)
	}
}

func TestMapTypeEdgeCases(t *testing.T) {
	cases := []struct {
		col  catalog.Column
		want string
	}{
		{catalog.Column{SourceType: "hierarchyid"}, "bytea"},
		{catalog.Column{SourceType: "geography"}, "bytea"},
		{catalog.Column{SourceType: "geometry"}, "bytea"},
		{catalog.Column{SourceType: "sql_variant"}, "text"},
		{catalog.Column{SourceType: "float", Precision: 24}, "real"},
		{catalog.Column{SourceType: "float", Precision: 25}, "double precision"},
		{catalog.Column{SourceType: "float", Precision: 0}, "double precision"},
		{catalog.Column{SourceType: "money"}, "numeric(19,4)"},
		{catalog.Column{SourceType: "smallmoney"}, "numeric(19,4)"},
		{catalog.Column{SourceType: "int", Identity: true}, "bigint"},
		{catalog.Column{SourceType: "tinyint", Identity: true}, "bigint"},
	}
	for _, tc := range cases {
		got, err := mapType(&tc.col)
		if err != nil {
			t.Errorf("mapType(%+v) error: %v", tc.col, err)
			continue
		}
		if got != tc.want {
			t.Errorf("mapType(%+v) = %q, want %q", tc.col, got, tc.want)
		}
	}
}
