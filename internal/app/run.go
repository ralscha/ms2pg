package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"ms2pg/internal/loader"
)

type Config struct {
	SourceDSN      string
	TargetDSN      string
	SchemaOnly     bool
	Verbose        bool
	IncludeSchemas []string
	IncludeTables  []string
	ExcludeSchemas []string
	ExcludeTables  []string
}

func Run(ctx context.Context, args []string) error {
	cfg, err := parseConfig(args)
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	if cfg.Verbose {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	runner := loader.Runner{
		Config: loader.Config{
			SourceDSN:      cfg.SourceDSN,
			TargetDSN:      cfg.TargetDSN,
			SchemaOnly:     cfg.SchemaOnly,
			IncludeSchemas: cfg.IncludeSchemas,
			IncludeTables:  cfg.IncludeTables,
			ExcludeSchemas: cfg.ExcludeSchemas,
			ExcludeTables:  cfg.ExcludeTables,
		},
		Logger: logger,
	}

	return runner.Run(ctx)
}

func parseConfig(args []string) (Config, error) {
	fs := flag.NewFlagSet("ms2pg", flag.ContinueOnError)
	fs.Usage = func() {}

	var cfg Config
	fs.StringVar(&cfg.SourceDSN, "source", "", "MSSQL connection string")
	fs.StringVar(&cfg.TargetDSN, "target", "", "PostgreSQL connection string")
	fs.BoolVar(&cfg.SchemaOnly, "schema-only", false, "create schemas, tables, and views without copying table data")
	fs.BoolVar(&cfg.Verbose, "verbose", false, "enable debug logging")
	includeSchemas := fs.String("include-schemas", "", "comma-separated schema filters; supports glob patterns")
	includeTables := fs.String("include-tables", "", "comma-separated table/view filters; supports glob patterns and schema.name forms")
	excludeSchemas := fs.String("exclude-schemas", "", "comma-separated schema filters to skip; supports glob patterns")
	excludeTables := fs.String("exclude-tables", "", "comma-separated table/view filters to skip; supports glob patterns and schema.name forms")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if cfg.SourceDSN == "" || cfg.TargetDSN == "" {
		return Config{}, errors.New("both -source and -target are required")
	}

	if len(fs.Args()) != 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	cfg.IncludeSchemas = parseCSVList(*includeSchemas)
	cfg.IncludeTables = parseCSVList(*includeTables)
	cfg.ExcludeSchemas = parseCSVList(*excludeSchemas)
	cfg.ExcludeTables = parseCSVList(*excludeTables)

	return cfg, nil
}

func parseCSVList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}

	return items
}
