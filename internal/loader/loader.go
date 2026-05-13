package loader

import (
	"context"
	"fmt"
	"log/slog"

	"ms2pg/internal/catalog"
	"ms2pg/internal/mapping"
	"ms2pg/internal/source/mssql"
	"ms2pg/internal/targetdb"
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
	logger := runner.logger()

	source, err := mssql.Open(runner.Config.SourceDSN)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() {
		_ = source.Close()
	}()

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

	logger.Info("introspecting source catalog")
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

	logger.Info("preparing target database")
	if err := target.PrepareDatabase(ctx, database); err != nil {
		return fmt.Errorf("prepare target: %w", err)
	}

	logger.Info("creating default constraints")
	if err := target.CreateDefaultConstraints(ctx, database); err != nil {
		return fmt.Errorf("create default constraints: %w", err)
	}

	if !runner.Config.SchemaOnly {
		for _, schema := range database.SortedSchemas() {
			for _, table := range schema.SortedTables() {
				logger.Info("copying table", "schema", table.Schema, "table", table.Name)
				if err := target.CopyTable(ctx, table, func(handleRow func([]any) error) error {
					return source.StreamTable(ctx, table, handleRow)
				}); err != nil {
					return err
				}
			}
		}

		logger.Info("resetting identity sequences")
		if err := target.ResetSequences(ctx, database); err != nil {
			return fmt.Errorf("reset sequences: %w", err)
		}
	}

	logger.Info("creating indexes")
	if err := target.CreateIndexes(ctx, database); err != nil {
		return fmt.Errorf("create indexes: %w", err)
	}

	logger.Info("creating unique constraints")
	if err := target.CreateUniqueConstraints(ctx, database); err != nil {
		return fmt.Errorf("create unique constraints: %w", err)
	}

	logger.Info("creating check constraints")
	if err := target.CreateCheckConstraints(ctx, database); err != nil {
		return fmt.Errorf("create check constraints: %w", err)
	}

	logger.Info("creating foreign keys")
	filterForeignKeys(database)
	if err := target.CreateForeignKeys(ctx, database); err != nil {
		return fmt.Errorf("create foreign keys: %w", err)
	}

	var pendingViews []*catalog.View
	for _, schema := range database.SortedSchemas() {
		pendingViews = append(pendingViews, schema.SortedViews()...)
	}
	for len(pendingViews) > 0 {
		var retryViews []*catalog.View
		var firstErr error
		for _, view := range pendingViews {
			logger.Info("creating view", "schema", view.Schema, "view", view.Name)
			if err := target.CreateView(ctx, view); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				retryViews = append(retryViews, view)
			}
		}
		if len(retryViews) == len(pendingViews) {
			return firstErr
		}
		pendingViews = retryViews
	}

	logger.Info("migration completed")
	return nil
}

func (runner Runner) logger() *slog.Logger {
	if runner.Logger != nil {
		return runner.Logger
	}
	return slog.Default()
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

func filterForeignKeys(database *catalog.Database) {
	tableSet := make(map[string]struct{})
	for _, schema := range database.Schemas {
		for _, table := range schema.Tables {
			tableSet[table.Schema+"."+table.Name] = struct{}{}
		}
	}
	for _, schema := range database.Schemas {
		for _, table := range schema.Tables {
			kept := table.ForeignKeys[:0]
			for _, fk := range table.ForeignKeys {
				if _, ok := tableSet[fk.ReferencedSchema+"."+fk.ReferencedTable]; ok {
					kept = append(kept, fk)
				}
			}
			table.ForeignKeys = kept
		}
	}
}
