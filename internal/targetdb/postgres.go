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

	"pgloader-go/internal/catalog"
)

var errUnsupportedViewDefinition = errors.New("unsupported MSSQL view definition")

var (
	createViewPattern        = regexp.MustCompile(`(?i)^\s*CREATE\s+VIEW\s+`)
	bracketIdentifierPattern = regexp.MustCompile(`\[([^\]]+)\]`)
	setDirectivePattern      = regexp.MustCompile(`(?i)^SET\s+(ANSI_NULLS|QUOTED_IDENTIFIER)\s+(ON|OFF)\s*;?$`)
	unicodeStringPattern     = regexp.MustCompile(`(?i)\bN'`)
	isNullPattern            = regexp.MustCompile(`(?i)\bISNULL\s*\(`)
	getDatePattern           = regexp.MustCompile(`(?i)\b(GETDATE|SYSDATETIME|CURRENT_TIMESTAMP)\s*\(\s*\)`)
	getUTCDatePattern        = regexp.MustCompile(`(?i)\bGETUTCDATE\s*\(\s*\)`)
	newIDPattern             = regexp.MustCompile(`(?i)\bNEWID\s*\(\s*\)`)
	lenPattern               = regexp.MustCompile(`(?i)\bLEN\s*\(`)
	dataLengthPattern        = regexp.MustCompile(`(?i)\bDATALENGTH\s*\(`)
)

type Target struct {
	pool *pgxpool.Pool
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
	needsPGCrypto := false
	for _, schema := range database.Schemas {
		for _, table := range schema.Tables {
			for _, column := range table.Columns {
				if column.Default == "gen_random_uuid()" {
					needsPGCrypto = true
					break
				}
			}
		}
	}

	if needsPGCrypto {
		if _, err := target.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
			return fmt.Errorf("create pgcrypto extension: %w", err)
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

func (target *Target) CreateIndexes(ctx context.Context, database *catalog.Database) error {
	for _, schema := range database.SortedSchemas() {
		for _, table := range schema.SortedTables() {
			for _, index := range table.Indexes {
				if _, err := target.pool.Exec(ctx, renderCreateIndex(table, index)); err != nil {
					return fmt.Errorf("create index %s on %s.%s: %w", index.Name, table.Schema, table.Name, err)
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

	copySource := newStreamCopySource(ctx, stream)

	_, err = conn.Conn().CopyFrom(
		ctx,
		pgx.Identifier{table.Schema, table.Name},
		columnNames,
		copySource,
	)
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
		parts = append(parts, "PRIMARY KEY ("+strings.Join(keys, ", ")+")")
	}

	return fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s.%s (\n  %s\n)",
		quoteIdentifier(table.Schema),
		quoteIdentifier(table.Name),
		strings.Join(parts, ",\n  "),
	)
}

func renderColumn(column *catalog.Column) string {
	parts := []string{quoteIdentifier(column.Name)}
	if column.Identity {
		parts = append(parts, "bigint GENERATED BY DEFAULT AS IDENTITY")
	} else {
		parts = append(parts, column.TargetType)
		if !column.Nullable {
			parts = append(parts, "NOT NULL")
		}
		if column.Default != "" {
			parts = append(parts, "DEFAULT "+column.Default)
		}
	}
	return strings.Join(parts, " ")
}

func renderCreateView(view *catalog.View) (string, error) {
	definition := normalizeViewDefinition(view.Definition)
	if err := validateViewDefinition(definition); err != nil {
		return "", err
	}
	return definition, nil
}

func renderCreateIndex(table *catalog.Table, index *catalog.Index) string {
	columns := make([]string, 0, len(index.Columns))
	for _, column := range index.Columns {
		columns = append(columns, quoteIdentifier(column))
	}

	unique := ""
	if index.Unique {
		unique = "UNIQUE "
	}

	statement := fmt.Sprintf(
		"CREATE %sINDEX IF NOT EXISTS %s ON %s.%s (%s)",
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

	if predicate := normalizeSQLExpression(index.Predicate); strings.TrimSpace(predicate) != "" {
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

	return statement
}

func normalizeReferentialAction(rule string) string {
	switch strings.ToUpper(strings.TrimSpace(rule)) {
	case "CASCADE":
		return "CASCADE"
	case "SET NULL":
		return "SET NULL"
	case "SET DEFAULT":
		return "SET DEFAULT"
	case "NO ACTION":
		return "NO ACTION"
	case "RESTRICT":
		return "RESTRICT"
	default:
		return ""
	}
}

func validateViewDefinition(definition string) error {
	upper := strings.ToUpper(definition)
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
		"IIF(",
	}

	for _, token := range unsupported {
		if strings.Contains(upper, token) {
			return fmt.Errorf("%w: contains %q", errUnsupportedViewDefinition, token)
		}
	}

	return nil
}

func normalizeViewDefinition(definition string) string {
	lines := strings.Split(strings.ReplaceAll(definition, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.EqualFold(trimmed, "GO") || setDirectivePattern.MatchString(trimmed) {
			continue
		}
		kept = append(kept, line)
	}

	normalized := strings.TrimSpace(strings.Join(kept, "\n"))
	normalized = createViewPattern.ReplaceAllString(normalized, "CREATE OR REPLACE VIEW ")
	normalized = bracketIdentifierPattern.ReplaceAllString(normalized, `"$1"`)
	normalized = normalizeSQLExpression(normalized)
	return normalized
}

func normalizeSQLExpression(expression string) string {
	normalized := expression
	normalized = bracketIdentifierPattern.ReplaceAllString(normalized, `"$1"`)
	normalized = unicodeStringPattern.ReplaceAllString(normalized, `'`)
	normalized = isNullPattern.ReplaceAllString(normalized, "COALESCE(")
	normalized = getDatePattern.ReplaceAllString(normalized, "CURRENT_TIMESTAMP")
	normalized = getUTCDatePattern.ReplaceAllString(normalized, "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')")
	normalized = newIDPattern.ReplaceAllString(normalized, "gen_random_uuid()")
	normalized = lenPattern.ReplaceAllString(normalized, "LENGTH(")
	normalized = dataLengthPattern.ReplaceAllString(normalized, "OCTET_LENGTH(")
	return normalized
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
