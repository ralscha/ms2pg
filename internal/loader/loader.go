package loader

import (
	"context"
	"fmt"
	"log/slog"

	"pgloader-go/internal/catalog"
	"pgloader-go/internal/mapping"
	"pgloader-go/internal/source/mssql"
	"pgloader-go/internal/targetdb"
)

type Config struct {
	SourceDSN      string
	TargetDSN      string
	SchemaOnly     bool
	IncludeSchemas []string
	IncludeTables  []string
	ExcludeSchemas []string
	ExcludeTables  []string
}

type Runner struct {
	Config Config
	Logger *slog.Logger
}

func (runner Runner) Run(ctx context.Context) error {
	source, err := mssql.Open(runner.Config.SourceDSN)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer source.Close()

	target, err := targetdb.Open(ctx, runner.Config.TargetDSN)
	if err != nil {
		return fmt.Errorf("open target: %w", err)
	}
	defer target.Close()

	if err := source.Ping(ctx); err != nil {
		return fmt.Errorf("ping source: %w", err)
	}
	if err := target.Ping(ctx); err != nil {
		return fmt.Errorf("ping target: %w", err)
	}

	runner.Logger.Info("introspecting source catalog")
	database, err := source.Introspect(ctx, catalog.Filters{
		IncludeSchemas: runner.Config.IncludeSchemas,
		IncludeTables:  runner.Config.IncludeTables,
		ExcludeSchemas: runner.Config.ExcludeSchemas,
		ExcludeTables:  runner.Config.ExcludeTables,
	})
	if err != nil {
		return fmt.Errorf("introspect source: %w", err)
	}

	if err := mapCatalog(database); err != nil {
		return fmt.Errorf("map source catalog: %w", err)
	}

	runner.Logger.Info("preparing target database")
	if err := target.PrepareDatabase(ctx, database); err != nil {
		return fmt.Errorf("prepare target: %w", err)
	}

	if !runner.Config.SchemaOnly {
		for _, schema := range database.SortedSchemas() {
			for _, table := range schema.SortedTables() {
				runner.Logger.Info("copying table", "schema", table.Schema, "table", table.Name)
				if err := target.CopyTable(ctx, table, func(handleRow func([]any) error) error {
					return source.StreamTable(ctx, table, handleRow)
				}); err != nil {
					return err
				}
			}
		}
	}

	runner.Logger.Info("creating indexes")
	if err := target.CreateIndexes(ctx, database); err != nil {
		return fmt.Errorf("create indexes: %w", err)
	}

	runner.Logger.Info("creating foreign keys")
	if err := target.CreateForeignKeys(ctx, database); err != nil {
		return fmt.Errorf("create foreign keys: %w", err)
	}

	for _, schema := range database.SortedSchemas() {
		for _, view := range schema.SortedViews() {
			runner.Logger.Info("creating view", "schema", view.Schema, "view", view.Name)
			if err := target.CreateView(ctx, view); err != nil {
				return err
			}
		}
	}

	runner.Logger.Info("migration completed")
	return nil
}

func mapCatalog(database *catalog.Database) error {
	for _, schema := range database.Schemas {
		for _, table := range schema.Tables {
			if err := mapping.Apply(table); err != nil {
				return err
			}
		}
	}
	return nil
}
