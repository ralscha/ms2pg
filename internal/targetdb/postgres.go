package targetdb

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ms2pg/internal/catalog"
	"ms2pg/internal/sqlrewrite"
)

var errUnsupportedViewDefinition = errors.New("unsupported MSSQL view definition")
var errUnsupportedIndexDefinition = errors.New("unsupported MSSQL index definition")
var errUnsupportedConstraintState = errors.New("unsupported MSSQL constraint state")
var errUnsupportedCheckConstraintDefinition = errors.New("unsupported MSSQL check constraint definition")
var errUnsupportedDefaultConstraintDefinition = errors.New("unsupported MSSQL default constraint definition")
var errInvalidUniqueConstraint = errors.New("invalid unique constraint metadata")

const maxPostgresIdentifierBytes = 63

var (
	viewHeaderPattern    = regexp.MustCompile(`(?is)^\s*(?:CREATE(?:\s+OR\s+ALTER)?|ALTER)\s+VIEW\s+(?:(?:\[(?:[^\]]|\]\])+\]|"(?:""|[^"])+"|[A-Z_@#][A-Z0-9_@$#]*)\s*\.\s*)?(?:\[(?:[^\]]|\]\])+\]|"(?:""|[^"])+"|[A-Z_@#][A-Z0-9_@$#]*)(\s*\([^)]*\))?(?:\s+WITH\s+SCHEMABINDING)?\s+AS\b`)
	setDirectivePattern  = regexp.MustCompile(`(?i)^SET\s+(ANSI_NULLS|QUOTED_IDENTIFIER)\s+(ON|OFF)\s*;?$`)
	isNullPattern        = regexp.MustCompile(`(?i)\bISNULL\s*\(`)
	getDatePattern       = regexp.MustCompile(`(?i)\b(GETDATE|SYSDATETIME)\b\s*\(\s*\)`)
	getUTCDatePattern    = regexp.MustCompile(`(?i)\b(GETUTCDATE|SYSUTCDATETIME)\b\s*\(\s*\)`)
	getOffsetDatePattern = regexp.MustCompile(`(?i)\bSYSDATETIMEOFFSET\b\s*\(\s*\)`)
	newIDPattern         = regexp.MustCompile(`(?i)\b(NEWID|NEWSEQUENTIALID)\b\s*\(\s*\)`)
	lenPattern           = regexp.MustCompile(`(?i)\bLEN\s*\(\s*([^)]+?)\s*\)`)
	dataLengthPattern    = regexp.MustCompile(`(?i)\bDATALENGTH\s*\(`)
	charIndexPattern     = regexp.MustCompile(`(?i)\bCHARINDEX\s*\(\s*([^,]+?)\s*,\s*([^,)]+?)\s*\)`)
	dateAddPattern       = regexp.MustCompile(`(?i)\bDATEADD\s*\(\s*(year|yy|yyyy|quarter|qq|q|month|mm|m|dayofyear|dy|y|day|dd|d|week|wk|ww|hour|hh|minute|mi|n|second|ss|s|millisecond|ms)\s*,\s*([^,]+?)\s*,\s*([^)]+?)\s*\)`)
	dateDiffPattern      = regexp.MustCompile(`(?i)\bDATEDIFF\s*\(\s*(year|yy|yyyy|quarter|qq|q|month|mm|m|dayofyear|dy|y|day|dd|d|week|wk|ww|hour|hh|minute|mi|n|second|ss|s|millisecond|ms)\s*,\s*([^,]+?)\s*,\s*([^)]+?)\s*\)`)
	iifPattern           = regexp.MustCompile(`(?i)\bIIF\s*\(\s*([^,]+?)\s*,\s*([^,]+?)\s*,\s*([^)]+?)\s*\)`)
	stuffPattern         = regexp.MustCompile(`(?i)\bSTUFF\s*\(\s*([^,]+?)\s*,\s*(\d+)\s*,\s*(\d+)\s*,\s*([^)]+?)\s*\)`)
	collatePattern       = regexp.MustCompile(`(?i)\bCOLLATE\s+(?:\[(?:[^\]]|\]\])+\]|"(?:""|[^"])+"|[A-Z0-9_]+)`)
	replicatePattern     = regexp.MustCompile(`(?i)\bREPLICATE\s*\(`)
	spacePattern         = regexp.MustCompile(`(?i)\bSPACE\s*\(\s*([^)]+?)\s*\)`)
	convertSafePattern   = regexp.MustCompile(`(?i)\bCONVERT\s*\(\s*(varchar|nvarchar|nchar|char|text|ntext|uniqueidentifier|sysname|money|smallmoney|integer|int|bigint|smallint|tinyint|float|real|bit|date|datetime2|smalldatetime|datetime|datetimeoffset|numeric|decimal)\s*(?:\(\s*[\d,\s]+\s*\))?\s*,\s*([^,)]+?)\s*\)`)
	logSingleArgPattern  = regexp.MustCompile(`(?i)\bLOG\s*\(\s*([^,)]+?)\s*\)`)
	castTypePattern      = regexp.MustCompile(`(?i)\bCAST\s*\(\s*(.+?)\s+AS\s+(datetimeoffset|datetime2|smalldatetime|datetime|smallmoney|money|uniqueidentifier|sysname|nvarchar|nchar|ntext|tinyint|bit)\s*(?:\([^)]*\))?\s*\)`)
	nextValueForPattern  = regexp.MustCompile(`(?i)\bNEXT\s+VALUE\s+FOR\b`)
	sqlFunctionPattern   = regexp.MustCompile(`(?i)\b([A-Z_][A-Z0-9_]*)\(`)
	hexLiteralPattern    = regexp.MustCompile(`^[0-9A-Fa-f]+$`)
	portableSQLFunctions = map[string]struct{}{
		"ABS":             {},
		"CASE":            {},
		"CAST":            {},
		"CEILING":         {},
		"COALESCE":        {},
		"CONCAT":          {},
		"EXP":             {},
		"EXTRACT":         {},
		"FLOOR":           {},
		"GEN_RANDOM_UUID": {},
		"LEFT":            {},
		"LENGTH":          {},
		"LN":              {},
		"LOWER":           {},
		"LTRIM":           {},
		"NULLIF":          {},
		"OCTET_LENGTH":    {},
		"OVERLAY":         {},
		"PI":              {},
		"POSITION":        {},
		"POWER":           {},
		"REPEAT":          {},
		"RIGHT":           {},
		"ROUND":           {},
		"RTRIM":           {},
		"SQRT":            {},
		"SUBSTRING":       {},
		"TRIM":            {},
		"TRUNC":           {},
		"UPPER":           {},
	}
)

type Target struct {
	pool *pgxpool.Pool
}

// Validate checks every definition that can be validated without connecting to
// PostgreSQL. Running this before target creation avoids leaving a partially
// migrated database for known unsupported SQL Server constructs.
func Validate(database *catalog.Database) error {
	if database == nil {
		return errors.New("missing source catalog")
	}
	if err := validateCatalogMetadata(database); err != nil {
		return err
	}

	for schemaName, relations := range targetRelationNames(database) {
		seen := make(map[string]string)
		for _, relation := range relations {
			if previous, exists := seen[relation.name]; exists {
				return fmt.Errorf("target name collision in schema %s: %s and %s both require relation name %q", schemaName, previous, relation.kind, relation.name)
			}
			seen[relation.name] = relation.kind
		}
	}

	for _, schema := range database.SortedSchemas() {
		for _, table := range schema.SortedTables() {
			for _, index := range table.Indexes {
				if index.Disabled {
					return fmt.Errorf("index %s on %s.%s: %w: disabled indexes cannot be preserved", index.Name, table.Schema, table.Name, errUnsupportedIndexDefinition)
				}
				if index.SourceType != 0 && index.SourceType != 1 && index.SourceType != 2 {
					return fmt.Errorf("index %s on %s.%s: %w: SQL Server index type %d is not a rowstore index", index.Name, table.Schema, table.Name, errUnsupportedIndexDefinition, index.SourceType)
				}
				if len(index.Columns) == 0 {
					return fmt.Errorf("index %s on %s.%s: %w: no portable key columns", index.Name, table.Schema, table.Name, errUnsupportedIndexDefinition)
				}
				if predicate, ok := normalizeIndexPredicate(index.Predicate); !ok {
					return fmt.Errorf("index %s on %s.%s: %w: unsupported predicate", index.Name, table.Schema, table.Name, errUnsupportedIndexDefinition)
				} else if !isPortableIndexPredicate(normalizeBooleanComparisons(table, predicate)) {
					return fmt.Errorf("index %s on %s.%s: %w: unsupported predicate", index.Name, table.Schema, table.Name, errUnsupportedIndexDefinition)
				}
			}
			for _, uniqueConstraint := range table.UniqueConstraints {
				if err := validateUniqueConstraint(uniqueConstraint); err != nil {
					return fmt.Errorf("unique constraint %s on %s.%s: %w", uniqueConstraint.Name, table.Schema, table.Name, err)
				}
			}
			for _, checkConstraint := range table.CheckConstraints {
				if checkConstraint.Disabled {
					return fmt.Errorf("check constraint %s on %s.%s: %w: disabled constraints cannot be preserved", checkConstraint.Name, table.Schema, table.Name, errUnsupportedConstraintState)
				}
				definition := normalizeBooleanComparisons(table, normalizeSQLExpression(checkConstraint.Definition))
				if err := validateCheckConstraintDefinition(definition); err != nil {
					return fmt.Errorf("check constraint %s on %s.%s: %w", checkConstraint.Name, table.Schema, table.Name, err)
				}
			}
			for _, defaultConstraint := range table.DefaultConstraints {
				definition := normalizeDefaultDefinition(table, defaultConstraint)
				if err := validateDefaultConstraintDefinition(definition); err != nil {
					return fmt.Errorf("default constraint %s on %s.%s: %w", defaultConstraint.Name, table.Schema, table.Name, err)
				}
			}
			for _, foreignKey := range table.ForeignKeys {
				if foreignKey.Disabled {
					return fmt.Errorf("foreign key %s on %s.%s: %w: disabled constraints cannot be preserved", foreignKey.Name, table.Schema, table.Name, errUnsupportedConstraintState)
				}
				if len(foreignKey.Columns) == 0 || len(foreignKey.Columns) != len(foreignKey.ReferencedColumns) {
					return fmt.Errorf("foreign key %s on %s.%s: invalid column metadata", foreignKey.Name, table.Schema, table.Name)
				}
			}
		}
		for _, view := range schema.SortedViews() {
			if _, err := renderCreateView(view); err != nil {
				return fmt.Errorf("view %s.%s: %w", view.Schema, view.Name, err)
			}
		}
	}

	return nil
}

func validateCatalogMetadata(database *catalog.Database) error {
	for schemaIndex, schema := range database.Schemas {
		if schema == nil {
			return fmt.Errorf("invalid source catalog: schema %d is missing", schemaIndex)
		}
		if err := validateIdentifier("schema", schema.Name); err != nil {
			return err
		}

		for tableIndex, table := range schema.Tables {
			if table == nil {
				return fmt.Errorf("invalid source catalog: table %d in schema %q is missing", tableIndex, schema.Name)
			}
			if table.Schema != schema.Name {
				return fmt.Errorf("invalid source catalog: table %q belongs to schema %q but is listed in schema %q", table.Name, table.Schema, schema.Name)
			}
			if err := validateIdentifier("table", table.Name); err != nil {
				return err
			}
			if table.PrimaryKeyName != "" {
				if err := validateIdentifier("primary key", table.PrimaryKeyName); err != nil {
					return err
				}
			}
			for columnIndex, column := range table.Columns {
				if column == nil {
					return fmt.Errorf("invalid source catalog: column %d on %s.%s is missing", columnIndex, table.Schema, table.Name)
				}
				if err := validateIdentifier("column", column.Name); err != nil {
					return err
				}
			}
			for indexPosition, index := range table.Indexes {
				if index == nil {
					return fmt.Errorf("invalid source catalog: index %d on %s.%s is missing", indexPosition, table.Schema, table.Name)
				}
				if err := validateIdentifier("index", index.Name); err != nil {
					return err
				}
			}
			for constraintIndex, constraint := range table.UniqueConstraints {
				if constraint == nil {
					return fmt.Errorf("invalid source catalog: unique constraint %d on %s.%s is missing", constraintIndex, table.Schema, table.Name)
				}
				if err := validateIdentifier("unique constraint", constraint.Name); err != nil {
					return err
				}
			}
			for constraintIndex, constraint := range table.CheckConstraints {
				if constraint == nil {
					return fmt.Errorf("invalid source catalog: check constraint %d on %s.%s is missing", constraintIndex, table.Schema, table.Name)
				}
				if err := validateIdentifier("check constraint", constraint.Name); err != nil {
					return err
				}
			}
			for constraintIndex, constraint := range table.DefaultConstraints {
				if constraint == nil {
					return fmt.Errorf("invalid source catalog: default constraint %d on %s.%s is missing", constraintIndex, table.Schema, table.Name)
				}
			}
			for constraintIndex, constraint := range table.ForeignKeys {
				if constraint == nil {
					return fmt.Errorf("invalid source catalog: foreign key %d on %s.%s is missing", constraintIndex, table.Schema, table.Name)
				}
				if err := validateIdentifier("foreign key", constraint.Name); err != nil {
					return err
				}
			}
		}

		for viewIndex, view := range schema.Views {
			if view == nil {
				return fmt.Errorf("invalid source catalog: view %d in schema %q is missing", viewIndex, schema.Name)
			}
			if view.Schema != schema.Name {
				return fmt.Errorf("invalid source catalog: view %q belongs to schema %q but is listed in schema %q", view.Name, view.Schema, schema.Name)
			}
			if err := validateIdentifier("view", view.Name); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateIdentifier(kind string, identifier string) error {
	if identifier == "" {
		return fmt.Errorf("invalid source catalog: %s identifier is empty", kind)
	}
	if len(identifier) > maxPostgresIdentifierBytes {
		return fmt.Errorf(
			"invalid source catalog: %s identifier %q is %d bytes; PostgreSQL supports at most %d bytes",
			kind,
			identifier,
			len(identifier),
			maxPostgresIdentifierBytes,
		)
	}
	return nil
}

func Open(ctx context.Context, connectionString string) (*Target, error) {
	pool, err := pgxpool.New(ctx, connectionString)
	if err != nil {
		return nil, err
	}
	return &Target{pool: pool}, nil
}

func (target *Target) Close() {
	target.pool.Close()
}

func (target *Target) Ping(ctx context.Context) error {
	return target.pool.Ping(ctx)
}

func (target *Target) PrepareDatabase(ctx context.Context, database *catalog.Database) error {
	if err := target.ensureRelationsAbsent(ctx, database); err != nil {
		return err
	}

	needsPGCrypto := false
	needsPG15 := false
	for _, schema := range database.Schemas {
		for _, table := range schema.Tables {
			for _, defaultConstraint := range table.DefaultConstraints {
				if defaultConstraint.Definition == "gen_random_uuid()" {
					needsPGCrypto = true
					break
				}
			}
			for _, uniqueConstraint := range table.UniqueConstraints {
				if needsNullsNotDistinct(table, uniqueConstraint.Columns) {
					needsPG15 = true
				}
			}
			for _, index := range table.Indexes {
				if index.Unique && needsNullsNotDistinct(table, index.Columns) {
					needsPG15 = true
				}
			}
		}
	}

	if needsPGCrypto || needsPG15 {
		var versionNum int
		if err := target.pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&versionNum); err != nil {
			return fmt.Errorf("check postgresql version: %w", err)
		}
		if needsPG15 && versionNum < 150000 {
			return fmt.Errorf("PostgreSQL 15 or newer is required to preserve SQL Server nullable-unique semantics")
		}
		if needsPGCrypto && versionNum < 130000 {
			if _, err := target.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
				return fmt.Errorf("create pgcrypto extension: %w", err)
			}
		}
	}

	for _, schema := range database.SortedSchemas() {
		if _, err := target.pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+quoteIdentifier(schema.Name)); err != nil {
			return fmt.Errorf("create schema %s: %w", schema.Name, err)
		}
		for _, table := range schema.SortedTables() {
			if _, err := target.pool.Exec(ctx, renderCreateTable(table)); err != nil {
				return fmt.Errorf("create table %s.%s: %w", table.Schema, table.Name, err)
			}
		}
	}

	return nil
}

type targetRelation struct {
	name string
	kind string
}

func targetRelationNames(database *catalog.Database) map[string][]targetRelation {
	relations := make(map[string][]targetRelation)
	for _, schema := range database.Schemas {
		for _, table := range schema.Tables {
			relations[schema.Name] = append(relations[schema.Name], targetRelation{name: table.Name, kind: "table " + table.Name})
			if table.PrimaryKeyName != "" {
				relations[schema.Name] = append(relations[schema.Name], targetRelation{name: table.PrimaryKeyName, kind: "primary key " + table.PrimaryKeyName})
			}
			for _, uniqueConstraint := range table.UniqueConstraints {
				relations[schema.Name] = append(relations[schema.Name], targetRelation{name: uniqueConstraint.Name, kind: "unique constraint " + uniqueConstraint.Name})
			}
			for _, index := range table.Indexes {
				relations[schema.Name] = append(relations[schema.Name], targetRelation{name: index.Name, kind: "index " + index.Name})
			}
		}
		for _, view := range schema.Views {
			relations[schema.Name] = append(relations[schema.Name], targetRelation{name: view.Name, kind: "view " + view.Name})
		}
	}
	return relations
}

func (target *Target) ensureRelationsAbsent(ctx context.Context, database *catalog.Database) error {
	for schemaName, relations := range targetRelationNames(database) {
		for _, relation := range relations {
			var exists bool
			if err := target.pool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1
					FROM pg_catalog.pg_class relation
					JOIN pg_catalog.pg_namespace namespace ON namespace.oid = relation.relnamespace
					WHERE namespace.nspname = $1 AND relation.relname = $2
				)`, schemaName, relation.name).Scan(&exists); err != nil {
				return fmt.Errorf("check target relation %s.%s: %w", schemaName, relation.name, err)
			}
			if exists {
				return fmt.Errorf("target relation %s.%s already exists; use an empty target for the selected objects", schemaName, relation.name)
			}
		}
	}
	return nil
}

func (target *Target) CreateIndexes(ctx context.Context, database *catalog.Database) error {
	for _, schema := range database.SortedSchemas() {
		for _, table := range schema.SortedTables() {
			for _, index := range table.Indexes {
				statement := renderCreateIndex(table, index)
				if statement == "" {
					continue
				}
				if _, err := target.pool.Exec(ctx, statement); err != nil {
					return fmt.Errorf("create index %s on %s.%s: %w", index.Name, table.Schema, table.Name, err)
				}
			}
		}
	}

	return nil
}

func (target *Target) CreateUniqueConstraints(ctx context.Context, database *catalog.Database) error {
	for _, schema := range database.SortedSchemas() {
		for _, table := range schema.SortedTables() {
			for _, uniqueConstraint := range table.UniqueConstraints {
				statement, err := renderCreateUniqueConstraint(table, uniqueConstraint)
				if err != nil {
					return fmt.Errorf("create unique constraint %s on %s.%s: %w", uniqueConstraint.Name, table.Schema, table.Name, err)
				}
				if _, err := target.pool.Exec(ctx, statement); err != nil {
					return fmt.Errorf("create unique constraint %s on %s.%s: %w", uniqueConstraint.Name, table.Schema, table.Name, err)
				}
			}
		}
	}

	return nil
}

func (target *Target) CreateForeignKeys(ctx context.Context, database *catalog.Database) error {
	for _, schema := range database.SortedSchemas() {
		for _, table := range schema.SortedTables() {
			for _, foreignKey := range table.ForeignKeys {
				if _, err := target.pool.Exec(ctx, renderCreateForeignKey(table, foreignKey)); err != nil {
					return fmt.Errorf("create foreign key %s on %s.%s: %w", foreignKey.Name, table.Schema, table.Name, err)
				}
			}
		}
	}

	return nil
}

func (target *Target) CreateCheckConstraints(ctx context.Context, database *catalog.Database) error {
	for _, schema := range database.SortedSchemas() {
		for _, table := range schema.SortedTables() {
			for _, checkConstraint := range table.CheckConstraints {
				statement, err := renderCreateCheckConstraint(table, checkConstraint)
				if err != nil {
					return fmt.Errorf("create check constraint %s on %s.%s: %w", checkConstraint.Name, table.Schema, table.Name, err)
				}
				if _, err := target.pool.Exec(ctx, statement); err != nil {
					return fmt.Errorf("create check constraint %s on %s.%s: %w", checkConstraint.Name, table.Schema, table.Name, err)
				}
			}
		}
	}

	return nil
}

func (target *Target) CreateDefaultConstraints(ctx context.Context, database *catalog.Database) error {
	for _, schema := range database.SortedSchemas() {
		for _, table := range schema.SortedTables() {
			for _, defaultConstraint := range table.DefaultConstraints {
				statement, err := renderCreateDefaultConstraint(table, defaultConstraint)
				if err != nil {
					return fmt.Errorf("create default constraint %s on %s.%s: %w", defaultConstraint.Name, table.Schema, table.Name, err)
				}
				if _, err := target.pool.Exec(ctx, statement); err != nil {
					return fmt.Errorf("create default constraint %s on %s.%s: %w", defaultConstraint.Name, table.Schema, table.Name, err)
				}
			}
		}
	}

	return nil
}

func (target *Target) CopyTable(ctx context.Context, table *catalog.Table, stream func(func([]any) error) error) error {
	conn, err := target.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire target connection: %w", err)
	}
	defer conn.Release()

	columnNames := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		columnNames = append(columnNames, column.Name)
	}

	copyCtx, cancelCopy := context.WithCancel(ctx)
	copySource := newStreamCopySource(copyCtx, stream)

	_, err = conn.Conn().CopyFrom(
		copyCtx,
		pgx.Identifier{table.Schema, table.Name},
		columnNames,
		copySource,
	)
	cancelCopy()
	if err != nil {
		return fmt.Errorf("copy %s.%s: %w", table.Schema, table.Name, err)
	}
	if err := copySource.Err(); err != nil {
		return fmt.Errorf("copy %s.%s: %w", table.Schema, table.Name, err)
	}

	return nil
}

type streamCopySource struct {
	ctx      context.Context
	rows     chan []any
	errMu    sync.Mutex
	err      error
	current  []any
	finished bool
}

func newStreamCopySource(ctx context.Context, stream func(func([]any) error) error) *streamCopySource {
	source := &streamCopySource{
		ctx:  ctx,
		rows: make(chan []any, 128),
	}

	go func() {
		defer close(source.rows)
		err := stream(func(row []any) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case source.rows <- row:
				return nil
			}
		})
		source.setErr(err)
	}()

	return source
}

func (source *streamCopySource) Next() bool {
	if source.finished {
		return false
	}

	select {
	case <-source.ctx.Done():
		source.setErr(source.ctx.Err())
		source.finished = true
		return false
	case row, ok := <-source.rows:
		if !ok {
			source.finished = true
			return false
		}
		source.current = row
		return true
	}
}

func (source *streamCopySource) Values() ([]any, error) {
	return source.current, nil
}

func (source *streamCopySource) Err() error {
	source.errMu.Lock()
	defer source.errMu.Unlock()
	return source.err
}

func (source *streamCopySource) setErr(err error) {
	if err == nil {
		return
	}
	source.errMu.Lock()
	defer source.errMu.Unlock()
	if source.err == nil {
		source.err = err
	}
}

func (target *Target) CreateView(ctx context.Context, view *catalog.View) error {
	statement, err := renderCreateView(view)
	if err != nil {
		return fmt.Errorf("create view %s.%s: %w", view.Schema, view.Name, err)
	}
	if _, err := target.pool.Exec(ctx, statement); err != nil {
		return fmt.Errorf("create view %s.%s: %w", view.Schema, view.Name, err)
	}
	return nil
}

func (target *Target) ResetSequences(ctx context.Context, database *catalog.Database) error {
	for _, schema := range database.SortedSchemas() {
		for _, table := range schema.SortedTables() {
			for _, column := range table.Columns {
				if !column.Identity {
					continue
				}
				aggregate := "MAX"
				if column.IdentityIncrement < 0 {
					aggregate = "MIN"
				}
				var rowCount int64
				var extremeValue *int64
				query := fmt.Sprintf(
					`SELECT COUNT(*), %s(%s) FROM %s.%s`,
					aggregate,
					quoteIdentifier(column.Name),
					quoteIdentifier(table.Schema),
					quoteIdentifier(table.Name),
				)
				if err := target.pool.QueryRow(ctx, query).Scan(&rowCount, &extremeValue); err != nil {
					return fmt.Errorf("get identity state %s.%s.%s: %w", table.Schema, table.Name, column.Name, err)
				}

				lastValue := column.IdentitySeed
				isCalled := false
				if rowCount > 0 {
					if extremeValue == nil {
						return fmt.Errorf("get identity state %s.%s.%s: non-empty table has no identity value", table.Schema, table.Name, column.Name)
					}
					lastValue = *extremeValue
					isCalled = true
				}

				tableIdentifier := pgx.Identifier{table.Schema, table.Name}.Sanitize()
				if _, err := target.pool.Exec(
					ctx,
					`SELECT setval(pg_get_serial_sequence($1, $2)::regclass, $3, $4)`,
					tableIdentifier,
					column.Name,
					lastValue,
					isCalled,
				); err != nil {
					return fmt.Errorf("reset sequence %s.%s.%s: %w", table.Schema, table.Name, column.Name, err)
				}
			}
		}
	}
	return nil
}

func renderCreateTable(table *catalog.Table) string {
	parts := make([]string, 0, len(table.Columns)+1)
	for _, column := range table.Columns {
		parts = append(parts, renderColumn(column))
	}
	if len(table.PrimaryKey) > 0 {
		keys := make([]string, 0, len(table.PrimaryKey))
		for _, key := range table.PrimaryKey {
			keys = append(keys, quoteIdentifier(key))
		}
		primaryKey := "PRIMARY KEY (" + strings.Join(keys, ", ") + ")"
		if table.PrimaryKeyName != "" {
			primaryKey = "CONSTRAINT " + quoteIdentifier(table.PrimaryKeyName) + " " + primaryKey
		}
		parts = append(parts, primaryKey)
	}

	return fmt.Sprintf(
		"CREATE TABLE %s.%s (\n  %s\n)",
		quoteIdentifier(table.Schema),
		quoteIdentifier(table.Name),
		strings.Join(parts, ",\n  "),
	)
}

func renderColumn(column *catalog.Column) string {
	parts := []string{quoteIdentifier(column.Name)}
	if column.Identity {
		parts = append(parts, column.TargetType, "GENERATED BY DEFAULT AS IDENTITY", identitySequenceOptions(column))
	} else {
		parts = append(parts, column.TargetType)
		if !column.Nullable {
			parts = append(parts, "NOT NULL")
		}
	}
	return strings.Join(parts, " ")
}

func identitySequenceOptions(column *catalog.Column) string {
	increment := column.IdentityIncrement
	if increment == 0 {
		increment = 1
	}

	minimum, maximum := identityBounds(column)
	return fmt.Sprintf(
		"(INCREMENT BY %d MINVALUE %d MAXVALUE %d START WITH %d)",
		increment,
		minimum,
		maximum,
		column.IdentitySeed,
	)
}

func identityBounds(column *catalog.Column) (int64, int64) {
	switch strings.ToLower(column.SourceType) {
	case "tinyint":
		return 0, 255
	case "smallint":
		return -32768, 32767
	case "int":
		return -2147483648, 2147483647
	default:
		return -9223372036854775808, 9223372036854775807
	}
}

func renderCreateView(view *catalog.View) (string, error) {
	definition, err := normalizeViewDefinition(view)
	if err != nil {
		return "", err
	}
	if err := validateViewDefinition(definition); err != nil {
		return "", err
	}
	return definition, nil
}

func renderCreateIndex(table *catalog.Table, index *catalog.Index) string {
	predicate, ok := normalizeIndexPredicate(index.Predicate)
	if !ok {
		return ""
	}
	predicate = normalizeBooleanComparisons(table, predicate)

	columns := make([]string, 0, len(index.Columns))
	for i, column := range index.Columns {
		col := quoteIdentifier(column)
		if i < len(index.DescendingColumns) && index.DescendingColumns[i] {
			col += " DESC"
		}
		columns = append(columns, col)
	}

	unique := ""
	if index.Unique {
		unique = "UNIQUE "
	}

	statement := fmt.Sprintf(
		"CREATE %sINDEX %s ON %s.%s (%s)",
		unique,
		quoteIdentifier(index.Name),
		quoteIdentifier(table.Schema),
		quoteIdentifier(table.Name),
		strings.Join(columns, ", "),
	)

	if len(index.IncludedColumns) > 0 {
		included := make([]string, 0, len(index.IncludedColumns))
		for _, column := range index.IncludedColumns {
			included = append(included, quoteIdentifier(column))
		}
		statement += " INCLUDE (" + strings.Join(included, ", ") + ")"
	}

	if index.Unique && needsNullsNotDistinct(table, index.Columns) {
		statement += " NULLS NOT DISTINCT"
	}

	if predicate != "" {
		statement += " WHERE " + predicate
	}

	return statement
}

func renderCreateForeignKey(table *catalog.Table, foreignKey *catalog.ForeignKey) string {
	columns := make([]string, 0, len(foreignKey.Columns))
	for _, column := range foreignKey.Columns {
		columns = append(columns, quoteIdentifier(column))
	}

	referencedColumns := make([]string, 0, len(foreignKey.ReferencedColumns))
	for _, column := range foreignKey.ReferencedColumns {
		referencedColumns = append(referencedColumns, quoteIdentifier(column))
	}

	statement := fmt.Sprintf(
		"ALTER TABLE %s.%s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s.%s (%s)",
		quoteIdentifier(table.Schema),
		quoteIdentifier(table.Name),
		quoteIdentifier(foreignKey.Name),
		strings.Join(columns, ", "),
		quoteIdentifier(foreignKey.ReferencedSchema),
		quoteIdentifier(foreignKey.ReferencedTable),
		strings.Join(referencedColumns, ", "),
	)

	if action := normalizeReferentialAction(foreignKey.UpdateRule); action != "" {
		statement += " ON UPDATE " + action
	}
	if action := normalizeReferentialAction(foreignKey.DeleteRule); action != "" {
		statement += " ON DELETE " + action
	}
	if foreignKey.NotTrusted {
		statement += " NOT VALID"
	}

	return statement
}

func renderCreateUniqueConstraint(table *catalog.Table, uniqueConstraint *catalog.UniqueConstraint) (string, error) {
	if err := validateUniqueConstraint(uniqueConstraint); err != nil {
		return "", err
	}

	columns := make([]string, 0, len(uniqueConstraint.Columns))
	for _, column := range uniqueConstraint.Columns {
		columns = append(columns, quoteIdentifier(column))
	}

	uniqueClause := "UNIQUE"
	if needsNullsNotDistinct(table, uniqueConstraint.Columns) {
		uniqueClause += " NULLS NOT DISTINCT"
	}

	return fmt.Sprintf(
		"ALTER TABLE %s.%s ADD CONSTRAINT %s %s (%s)",
		quoteIdentifier(table.Schema),
		quoteIdentifier(table.Name),
		quoteIdentifier(uniqueConstraint.Name),
		uniqueClause,
		strings.Join(columns, ", "),
	), nil
}

func needsNullsNotDistinct(table *catalog.Table, columnNames []string) bool {
	for _, columnName := range columnNames {
		found := false
		for _, column := range table.Columns {
			if column.Name != columnName {
				continue
			}
			found = true
			if column.Nullable {
				return true
			}
			break
		}
		if !found {
			return true
		}
	}
	return false
}

func renderCreateCheckConstraint(table *catalog.Table, checkConstraint *catalog.CheckConstraint) (string, error) {
	definition := normalizeBooleanComparisons(table, normalizeSQLExpression(checkConstraint.Definition))
	if err := validateCheckConstraintDefinition(definition); err != nil {
		return "", err
	}

	statement := fmt.Sprintf(
		"ALTER TABLE %s.%s ADD CONSTRAINT %s CHECK (%s)",
		quoteIdentifier(table.Schema),
		quoteIdentifier(table.Name),
		quoteIdentifier(checkConstraint.Name),
		definition,
	)
	if checkConstraint.NotTrusted {
		statement += " NOT VALID"
	}
	return statement, nil
}

func renderCreateDefaultConstraint(table *catalog.Table, defaultConstraint *catalog.DefaultConstraint) (string, error) {
	definition := normalizeDefaultDefinition(table, defaultConstraint)
	if err := validateDefaultConstraintDefinition(definition); err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"ALTER TABLE %s.%s ALTER COLUMN %s SET DEFAULT %s",
		quoteIdentifier(table.Schema),
		quoteIdentifier(table.Name),
		quoteIdentifier(defaultConstraint.Column),
		definition,
	), nil
}

func normalizeDefaultDefinition(table *catalog.Table, defaultConstraint *catalog.DefaultConstraint) string {
	definition := normalizeSQLExpression(defaultConstraint.Definition)
	column := findColumn(table, defaultConstraint.Column)
	if column == nil {
		return definition
	}

	value := trimExpressionParens(definition)
	switch column.TargetType {
	case "boolean":
		switch value {
		case "0":
			return "FALSE"
		case "1":
			return "TRUE"
		}
	case "bytea":
		if len(value) >= 2 && strings.EqualFold(value[:2], "0x") {
			hexValue := value[2:]
			if hexValue != "" && hexLiteralPattern.MatchString(hexValue) && len(hexValue)%2 == 0 {
				return `'\x` + hexValue + `'::bytea`
			}
		}
	}
	return definition
}

func normalizeBooleanComparisons(table *catalog.Table, definition string) string {
	protected := sqlrewrite.Protect(definition, false)
	normalized := protected.SQL
	for _, column := range table.Columns {
		if column.TargetType != "boolean" {
			continue
		}

		identifier := regexp.QuoteMeta(quoteIdentifier(column.Name))
		rightValue := regexp.MustCompile(`(?i)(` + identifier + `\s*(?:=|<>|!=)\s*)(?:\(\s*([01])\s*\)|([01]))`)
		normalized = rightValue.ReplaceAllStringFunc(normalized, func(match string) string {
			parts := rightValue.FindStringSubmatch(match)
			value := parts[2]
			if value == "" {
				value = parts[3]
			}
			return parts[1] + booleanLiteral(value)
		})
		leftValue := regexp.MustCompile(`(?i)(?:\(\s*([01])\s*\)|([01]))(\s*(?:=|<>|!=)\s*` + identifier + `)`)
		normalized = leftValue.ReplaceAllStringFunc(normalized, func(match string) string {
			parts := leftValue.FindStringSubmatch(match)
			value := parts[1]
			if value == "" {
				value = parts[2]
			}
			return booleanLiteral(value) + parts[3]
		})
	}
	return protected.Restore(normalized)
}

func booleanLiteral(value string) string {
	if value == "1" {
		return "TRUE"
	}
	return "FALSE"
}

func findColumn(table *catalog.Table, name string) *catalog.Column {
	for _, column := range table.Columns {
		if column.Name == name {
			return column
		}
	}
	return nil
}

func trimExpressionParens(value string) string {
	value = strings.TrimSpace(value)
	for len(value) >= 2 && value[0] == '(' && value[len(value)-1] == ')' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func normalizeReferentialAction(rule string) string {
	switch strings.ToUpper(strings.TrimSpace(rule)) {
	case "CASCADE":
		return "CASCADE"
	case "SET NULL":
		return "SET NULL"
	case "SET_NULL":
		return "SET NULL"
	case "SET DEFAULT":
		return "SET DEFAULT"
	case "SET_DEFAULT":
		return "SET DEFAULT"
	case "NO ACTION":
		return "NO ACTION"
	case "NO_ACTION":
		return "NO ACTION"
	case "RESTRICT":
		return "RESTRICT"
	default:
		return ""
	}
}

func validateViewDefinition(definition string) error {
	validationSQL := sqlForValidation(definition)
	if nextValueForPattern.MatchString(validationSQL) {
		return fmt.Errorf("%w: contains %q", errUnsupportedViewDefinition, "NEXT VALUE FOR")
	}
	upper := strings.ToUpper(validationSQL)
	unsupported := []string{
		"TOP ",
		"TOP(",
		"WITH ENCRYPTION",
		"WITH CHECK OPTION",
		"CROSS APPLY",
		"OUTER APPLY",
		"TRY_CONVERT(",
		"TRY_CAST(",
		"CHARINDEX(",
		"CONVERT(",
		"DATALENGTH(",
		"DATEADD(",
		"DATEDIFF(",
		"GETDATE(",
		"GETUTCDATE(",
		"IIF(",
		"ISNULL(",
		"LEN(",
		"LOG(",
		"NEWID(",
		"NEWSEQUENTIALID(",
		"REPLICATE(",
		"SPACE(",
		"STUFF(",
		"SYSDATETIME(",
		"SYSDATETIMEOFFSET(",
		"SYSUTCDATETIME(",
	}

	for _, token := range unsupported {
		if strings.Contains(upper, token) {
			return fmt.Errorf("%w: contains %q", errUnsupportedViewDefinition, token)
		}
	}

	return nil
}

func validateUniqueConstraint(uniqueConstraint *catalog.UniqueConstraint) error {
	if uniqueConstraint == nil {
		return fmt.Errorf("%w: missing constraint", errInvalidUniqueConstraint)
	}
	if strings.TrimSpace(uniqueConstraint.Name) == "" {
		return fmt.Errorf("%w: missing name", errInvalidUniqueConstraint)
	}
	if len(uniqueConstraint.Columns) == 0 {
		return fmt.Errorf("%w: no columns for %s", errInvalidUniqueConstraint, uniqueConstraint.Name)
	}
	for _, column := range uniqueConstraint.Columns {
		if strings.TrimSpace(column) == "" {
			return fmt.Errorf("%w: blank column in %s", errInvalidUniqueConstraint, uniqueConstraint.Name)
		}
	}
	return nil
}

func validateCheckConstraintDefinition(definition string) error {
	if !isPortableCheckConstraintDefinition(definition) {
		upper := strings.ToUpper(sqlForValidation(definition))
		for _, token := range []string{"TRY_CONVERT(", "TRY_CAST(", "CONVERT(", "CROSS APPLY", "OUTER APPLY", "TOP ", "TOP("} {
			if strings.Contains(upper, token) {
				return fmt.Errorf("%w: contains %q", errUnsupportedCheckConstraintDefinition, token)
			}
		}
		return fmt.Errorf("%w: contains unsupported expression", errUnsupportedCheckConstraintDefinition)
	}
	return nil
}

func validateDefaultConstraintDefinition(definition string) error {
	if nextValueForPattern.MatchString(sqlForValidation(definition)) {
		return fmt.Errorf("%w: contains %q", errUnsupportedDefaultConstraintDefinition, "NEXT VALUE FOR")
	}
	if !isPortableDefaultConstraintDefinition(definition) {
		upper := strings.ToUpper(sqlForValidation(definition))
		for _, token := range []string{"TRY_CONVERT(", "TRY_CAST(", "CONVERT("} {
			if strings.Contains(upper, token) {
				return fmt.Errorf("%w: contains %q", errUnsupportedDefaultConstraintDefinition, token)
			}
		}
		return fmt.Errorf("%w: contains unsupported expression", errUnsupportedDefaultConstraintDefinition)
	}
	return nil
}

func normalizeViewDefinition(view *catalog.View) (string, error) {
	lines := strings.Split(strings.ReplaceAll(view.Definition, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.EqualFold(trimmed, "GO") || setDirectivePattern.MatchString(trimmed) {
			continue
		}
		kept = append(kept, line)
	}

	normalized := strings.TrimSpace(strings.Join(kept, "\n"))
	header := viewHeaderPattern.FindStringSubmatchIndex(normalized)
	if header == nil {
		return "", fmt.Errorf("%w: missing CREATE VIEW header", errUnsupportedViewDefinition)
	}

	columnList := ""
	if header[2] >= 0 {
		columnList = normalized[header[2]:header[3]]
	}
	normalized = "CREATE VIEW " + quoteIdentifier(view.Schema) + "." + quoteIdentifier(view.Name) + columnList + " AS" + normalized[header[1]:]
	normalized = normalizeSQLExpression(normalized)
	return normalized, nil
}

func normalizeSQLExpression(expression string) string {
	protected := sqlrewrite.Protect(expression, true)
	normalized := protected.SQL
	normalized = collatePattern.ReplaceAllString(normalized, "")
	normalized = normalizeBracketIdentifiers(normalized)
	protectedIdentifiers := sqlrewrite.ProtectIdentifiers(normalized)
	normalized = protectedIdentifiers.SQL
	normalized = isNullPattern.ReplaceAllString(normalized, "COALESCE(")
	normalized = getDatePattern.ReplaceAllString(normalized, "CURRENT_TIMESTAMP")
	normalized = getUTCDatePattern.ReplaceAllString(normalized, "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')")
	normalized = getOffsetDatePattern.ReplaceAllString(normalized, "CURRENT_TIMESTAMP")
	normalized = newIDPattern.ReplaceAllString(normalized, "gen_random_uuid()")
	normalized = lenPattern.ReplaceAllString(normalized, "LENGTH(RTRIM($1))")
	normalized = dataLengthPattern.ReplaceAllString(normalized, "OCTET_LENGTH(")
	normalized = charIndexPattern.ReplaceAllString(normalized, "POSITION($1 IN $2)")
	normalized = iifPattern.ReplaceAllStringFunc(normalized, translateIIF)
	normalized = stuffPattern.ReplaceAllStringFunc(normalized, translateSTUFF)
	normalized = dateAddPattern.ReplaceAllStringFunc(normalized, translateDATEADD)
	normalized = dateDiffPattern.ReplaceAllStringFunc(normalized, translateDATEDIFF)
	normalized = replicatePattern.ReplaceAllString(normalized, "REPEAT(")
	normalized = spacePattern.ReplaceAllStringFunc(normalized, translateSPACE)
	normalized = convertSafePattern.ReplaceAllStringFunc(normalized, translateCONVERTSafe)
	normalized = logSingleArgPattern.ReplaceAllString(normalized, "LN($1)")

	for {
		next := castTypePattern.ReplaceAllStringFunc(normalized, translateCAST)
		if next == normalized {
			break
		}
		normalized = next
	}
	return protected.Restore(protectedIdentifiers.Restore(normalized))
}

func normalizeBracketIdentifiers(input string) string {
	var output strings.Builder
	output.Grow(len(input))

	for index := 0; index < len(input); {
		if input[index] != '[' {
			output.WriteByte(input[index])
			index++
			continue
		}

		start := index
		index++
		var identifier strings.Builder
		closed := false
		for index < len(input) {
			if input[index] != ']' {
				identifier.WriteByte(input[index])
				index++
				continue
			}
			if index+1 < len(input) && input[index+1] == ']' {
				identifier.WriteByte(']')
				index += 2
				continue
			}
			index++
			closed = true
			break
		}

		if !closed {
			output.WriteString(input[start:])
			break
		}
		output.WriteString(quoteIdentifier(identifier.String()))
	}

	return output.String()
}

func translateIIF(s string) string {
	m := iifPattern.FindStringSubmatch(s)
	if len(m) != 4 {
		return s
	}
	return fmt.Sprintf("CASE WHEN %s THEN %s ELSE %s END", strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), strings.TrimSpace(m[3]))
}

func translateSTUFF(s string) string {
	m := stuffPattern.FindStringSubmatch(s)
	if len(m) != 5 {
		return s
	}
	str, start, length, replacement := strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), strings.TrimSpace(m[3]), strings.TrimSpace(m[4])
	return fmt.Sprintf("OVERLAY(%s PLACING %s FROM %s FOR %s)", str, replacement, start, length)
}

func translateDATEADD(s string) string {
	m := dateAddPattern.FindStringSubmatch(s)
	if len(m) != 4 {
		return s
	}
	unit, n, expr := strings.ToLower(m[1]), strings.TrimSpace(m[2]), strings.TrimSpace(m[3])
	interval := dateAddInterval(unit)
	if interval == "" {
		return s
	}
	// SQL Server truncates a fractional DATEADD number; multiplying a
	// PostgreSQL interval by it directly would retain the fraction.
	return fmt.Sprintf("(%s + (TRUNC((%s)::numeric)::double precision * INTERVAL '%s'))", expr, n, interval)
}

func translateDATEDIFF(s string) string {
	m := dateDiffPattern.FindStringSubmatch(s)
	if len(m) != 4 {
		return s
	}
	unit, start, end := strings.ToLower(m[1]), strings.TrimSpace(m[2]), strings.TrimSpace(m[3])
	switch unit {
	case "year", "yy", "yyyy":
		return fmt.Sprintf("(EXTRACT(YEAR FROM (%s)::date) - EXTRACT(YEAR FROM (%s)::date))::integer", end, start)
	case "quarter", "qq", "q":
		return fmt.Sprintf("((EXTRACT(YEAR FROM (%s)::date) * 4 + EXTRACT(QUARTER FROM (%s)::date)) - (EXTRACT(YEAR FROM (%s)::date) * 4 + EXTRACT(QUARTER FROM (%s)::date)))::integer", end, end, start, start)
	case "month", "mm", "m":
		return fmt.Sprintf("((EXTRACT(YEAR FROM (%s)::date) - EXTRACT(YEAR FROM (%s)::date)) * 12 + (EXTRACT(MONTH FROM (%s)::date) - EXTRACT(MONTH FROM (%s)::date)))::integer", end, start, end, start)
	case "day", "dd", "d", "dayofyear", "dy", "y":
		return fmt.Sprintf("((%s)::date - (%s)::date)", end, start)
	case "week", "wk", "ww":
		return fmt.Sprintf("((DATE_TRUNC('week', (%s)::date + 1)::date - DATE_TRUNC('week', (%s)::date + 1)::date) / 7)", end, start)
	case "hour", "hh":
		return fmt.Sprintf("(EXTRACT(EPOCH FROM (DATE_TRUNC('hour', (%s)::timestamp) - DATE_TRUNC('hour', (%s)::timestamp))) / 3600)::integer", end, start)
	case "minute", "mi", "n":
		return fmt.Sprintf("(EXTRACT(EPOCH FROM (DATE_TRUNC('minute', (%s)::timestamp) - DATE_TRUNC('minute', (%s)::timestamp))) / 60)::integer", end, start)
	case "second", "ss", "s":
		return fmt.Sprintf("EXTRACT(EPOCH FROM (DATE_TRUNC('second', (%s)::timestamp) - DATE_TRUNC('second', (%s)::timestamp)))::integer", end, start)
	case "millisecond", "ms":
		return fmt.Sprintf("(EXTRACT(EPOCH FROM (DATE_TRUNC('milliseconds', (%s)::timestamp) - DATE_TRUNC('milliseconds', (%s)::timestamp))) * 1000)::integer", end, start)
	default:
		return s
	}
}

func translateSPACE(s string) string {
	m := spacePattern.FindStringSubmatch(s)
	if len(m) != 2 {
		return s
	}
	return fmt.Sprintf("REPEAT(' ', %s)", strings.TrimSpace(m[1]))
}

func translateCONVERTSafe(s string) string {
	m := convertSafePattern.FindStringSubmatch(s)
	if len(m) != 3 {
		return s
	}
	pgType := mssqlTypeToPG(strings.ToLower(strings.TrimSpace(m[1])))
	if pgType == "" {
		return s
	}
	expr := strings.TrimSpace(m[2])
	return fmt.Sprintf("(%s)::%s", expr, pgType)
}

func translateCAST(s string) string {
	m := castTypePattern.FindStringSubmatch(s)
	if len(m) != 3 {
		return s
	}
	pgType := mssqlTypeToPG(strings.ToLower(strings.TrimSpace(m[2])))
	if pgType == "" {
		return s
	}
	return fmt.Sprintf("CAST(%s AS %s)", strings.TrimSpace(m[1]), pgType)
}

func mssqlTypeToPG(t string) string {
	switch t {
	case "varchar", "nvarchar", "char", "nchar", "text", "ntext":
		return "text"
	case "integer", "int":
		return "integer"
	case "bigint":
		return "bigint"
	case "smallint", "tinyint":
		return "smallint"
	case "float":
		return "double precision"
	case "real":
		return "real"
	case "bit":
		return "boolean"
	case "date":
		return "date"
	case "datetime", "datetime2", "smalldatetime":
		return "timestamp"
	case "datetimeoffset":
		return "timestamptz"
	case "uniqueidentifier":
		return "uuid"
	case "money", "smallmoney":
		return "numeric"
	case "sysname":
		return "text"
	case "numeric", "decimal":
		return "numeric"
	default:
		return ""
	}
}

func dateAddInterval(unit string) string {
	switch unit {
	case "year", "yy", "yyyy":
		return "1 year"
	case "quarter", "qq", "q":
		return "3 months"
	case "month", "mm", "m":
		return "1 month"
	case "dayofyear", "dy", "y", "day", "dd", "d":
		return "1 day"
	case "week", "wk", "ww":
		return "1 week"
	case "hour", "hh":
		return "1 hour"
	case "minute", "mi", "n":
		return "1 minute"
	case "second", "ss", "s":
		return "1 second"
	case "millisecond", "ms":
		return "1 millisecond"
	default:
		return ""
	}
}

func normalizeIndexPredicate(predicate string) (string, bool) {
	if strings.TrimSpace(predicate) == "" {
		return "", true
	}

	normalized := strings.TrimSpace(normalizeSQLExpression(predicate))
	if normalized == "" {
		return "", false
	}
	if !isPortableIndexPredicate(normalized) {
		return "", false
	}
	return normalized, true
}

func isPortableIndexPredicate(predicate string) bool {
	upper := strings.ToUpper(sqlForValidation(predicate))
	unsupported := []string{
		"TOP ",
		"TOP(",
		"WITH SCHEMABINDING",
		"WITH ENCRYPTION",
		"WITH CHECK OPTION",
		"CROSS APPLY",
		"OUTER APPLY",
		"TRY_CONVERT(",
		"TRY_CAST(",
	}

	for _, token := range unsupported {
		if strings.Contains(upper, token) {
			return false
		}
	}

	return hasOnlyPortableFunctions(upper)
}

func isPortableCheckConstraintDefinition(definition string) bool {
	upper := strings.ToUpper(sqlForValidation(definition))
	unsupported := []string{
		"TOP ",
		"TOP(",
		"CROSS APPLY",
		"OUTER APPLY",
		"TRY_CONVERT(",
		"TRY_CAST(",
		"CONVERT(",
	}

	for _, token := range unsupported {
		if strings.Contains(upper, token) {
			return false
		}
	}

	return hasOnlyPortableFunctions(upper)
}

func isPortableDefaultConstraintDefinition(definition string) bool {
	upper := strings.ToUpper(sqlForValidation(definition))
	unsupported := []string{
		"TRY_CONVERT(",
		"TRY_CAST(",
	}

	for _, token := range unsupported {
		if strings.Contains(upper, token) {
			return false
		}
	}

	return hasOnlyPortableFunctions(upper)
}

func hasOnlyPortableFunctions(sql string) bool {
	for _, match := range sqlFunctionPattern.FindAllStringSubmatch(sql, -1) {
		functionName := match[1]
		if _, ok := portableSQLFunctions[functionName]; ok {
			continue
		}
		return false
	}

	return true
}

func sqlForValidation(sql string) string {
	return sqlrewrite.ProtectIdentifiers(sql).SQL
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
