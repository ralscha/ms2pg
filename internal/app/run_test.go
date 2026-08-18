package app

import (
	"errors"
	"flag"
	"testing"
)

func TestParseConfigHelp(t *testing.T) {
	if _, err := parseConfig([]string{"-help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseConfig(-help) error = %v, want flag.ErrHelp", err)
	}
}

func TestParseConfigParsesFilterFlags(t *testing.T) {
	cfg, err := parseConfig([]string{
		"-source", "sqlserver://example",
		"-target", "postgres://example",
		"-include-schemas", "dbo, reporting",
		"-include-tables", "users,reporting.user_names,sales.*",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}

	if len(cfg.IncludeSchemas) != 2 || cfg.IncludeSchemas[0] != "dbo" || cfg.IncludeSchemas[1] != "reporting" {
		t.Fatalf("IncludeSchemas = %#v, want [dbo reporting]", cfg.IncludeSchemas)
	}
	if len(cfg.IncludeTables) != 3 || cfg.IncludeTables[0] != "users" || cfg.IncludeTables[1] != "reporting.user_names" || cfg.IncludeTables[2] != "sales.*" {
		t.Fatalf("IncludeTables = %#v, want parsed table filters", cfg.IncludeTables)
	}
}

func TestParseConfigParsesExcludeFilterFlags(t *testing.T) {
	cfg, err := parseConfig([]string{
		"-source", "sqlserver://example",
		"-target", "postgres://example",
		"-exclude-schemas", "sales,archive*",
		"-exclude-tables", "reporting.user_labels,temp_*",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}

	if len(cfg.ExcludeSchemas) != 2 || cfg.ExcludeSchemas[0] != "sales" || cfg.ExcludeSchemas[1] != "archive*" {
		t.Fatalf("ExcludeSchemas = %#v, want [sales archive*]", cfg.ExcludeSchemas)
	}
	if len(cfg.ExcludeTables) != 2 || cfg.ExcludeTables[0] != "reporting.user_labels" || cfg.ExcludeTables[1] != "temp_*" {
		t.Fatalf("ExcludeTables = %#v, want parsed exclude filters", cfg.ExcludeTables)
	}
}
