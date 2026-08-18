package mapping

import (
	"strings"
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

	if got := table.Columns[0].TargetType; got != "integer" {
		t.Fatalf("identity target type = %q, want integer", got)
	}
	if got := table.Columns[1].TargetType; got != "timestamp(0)" {
		t.Fatalf("datetime2 target type = %q, want timestamp(0)", got)
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
		{catalog.Column{SourceType: "smallmoney"}, "numeric(10,4)"},
		{catalog.Column{SourceType: "char", Length: 12}, "character(12)"},
		{catalog.Column{SourceType: "varchar", Length: 200}, "character varying(200)"},
		{catalog.Column{SourceType: "varchar", Length: -1}, "text"},
		{catalog.Column{SourceType: "nchar", Length: 24}, "character(12)"},
		{catalog.Column{SourceType: "nvarchar", Length: 200}, "character varying(100)"},
		{catalog.Column{SourceType: "datetime2", Scale: 7}, "timestamp(6)"},
		{catalog.Column{SourceType: "datetimeoffset", Scale: 3}, "timestamptz(3)"},
		{catalog.Column{SourceType: "time", Scale: 4}, "time(4)"},
		{catalog.Column{SourceType: "int", Identity: true}, "integer"},
		{catalog.Column{SourceType: "tinyint", Identity: true}, "smallint"},
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

func TestMapTypeRejectsUnsupportedIdentityType(t *testing.T) {
	column := &catalog.Column{SourceType: "numeric", Precision: 20, Scale: 0, Identity: true}
	if _, err := mapType(column); err == nil || !strings.Contains(err.Error(), "unsupported identity source type") {
		t.Fatalf("mapType() error = %v, want unsupported identity source type", err)
	}
}

func TestMapDefaultDoesNotRewriteStringContents(t *testing.T) {
	input := `(N'GETDATE() [not_an_identifier]')`
	if got, want := mapDefault(input), `'GETDATE() [not_an_identifier]'`; got != want {
		t.Fatalf("mapDefault(%q) = %q, want %q", input, got, want)
	}
}

func TestMapDefaultMapsDatetimeOffsetToTimestampWithTimeZone(t *testing.T) {
	if got, want := mapDefault(`SYSDATETIMEOFFSET()`), `CURRENT_TIMESTAMP`; got != want {
		t.Fatalf("mapDefault(SYSDATETIMEOFFSET()) = %q, want %q", got, want)
	}
}
