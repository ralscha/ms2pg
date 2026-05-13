package catalog

import "testing"

func TestFiltersMatchTablesAndViewsWithIncludesAndExcludes(t *testing.T) {
	filters := Filters{
		IncludeSchemas: []string{"dbo", "report*"},
		IncludeTables:  []string{"users", "reporting.user_*"},
		ExcludeSchemas: []string{"archive"},
		ExcludeTables:  []string{"reporting.user_metrics"},
	}

	if !filters.MatchesTable("DBO", "Users") {
		t.Fatal("MatchesTable returned false for case-insensitive unqualified include")
	}
	if !filters.MatchesView("reporting", "user_names") {
		t.Fatal("MatchesView returned false for qualified wildcard include")
	}
	if filters.MatchesTable("reporting", "user_metrics") {
		t.Fatal("MatchesTable returned true for excluded qualified object")
	}
	if filters.MatchesTable("archive", "users") {
		t.Fatal("MatchesTable returned true for excluded schema")
	}
	if filters.MatchesView("sales", "users") {
		t.Fatal("MatchesView returned true for schema outside include list")
	}
}

func TestMatchPatternRejectsInvalidGlob(t *testing.T) {
	if matchPattern("[", "dbo.users", "users") {
		t.Fatal("matchPattern returned true for invalid glob")
	}
}
