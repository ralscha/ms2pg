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
)

var errUnsupportedViewDefinition = errors.New("unsupported MSSQL view definition")
var errUnsupportedCheckConstraintDefinition = errors.New("unsupported MSSQL check constraint definition")
var errUnsupportedDefaultConstraintDefinition = errors.New("unsupported MSSQL default constraint definition")
var errInvalidUniqueConstraint = errors.New("invalid unique constraint metadata")

var (
	createViewPattern        = regexp.MustCompile(`(?i)^\s*CREATE\s+VIEW\s+`)
	bracketIdentifierPattern = regexp.MustCompile(`\[([^\]]+)\]`)
	setDirectivePattern      = regexp.MustCompile(`(?i)^SET\s+(ANSI_NULLS|QUOTED_IDENTIFIER)\s+(ON|OFF)\s*;?$`)
	unicodeStringPattern     = regexp.MustCompile(`(?i)\bN'`)
	isNullPattern            = regexp.MustCompile(`(?i)\bISNULL\s*\(`)
	getDatePattern           = regexp.MustCompile(`(?i)\b(GETDATE|SYSDATETIME|CURRENT_TIMESTAMP)\s*\(\s*\)`)
	getUTCDatePattern        = regexp.MustCompile(`(?i)\b(GETUTCDATE|SYSDATETIMEOFFSET)\s*\(\s*\)`)
	newIDPattern             = regexp.MustCompile(`(?i)\b(NEWID|NEWSEQUENTIALID)\s*\(\s*\)`)
	lenPattern               = regexp.MustCompile(`(?i)\bLEN\s*\(`)
	dataLengthPattern        = regexp.MustCompile(`(?i)\bDATALENGTH\s*\(`)
	charIndexPattern         = regexp.MustCompile(`(?i)\bCHARINDEX\s*\(\s*([^,]+?)\s*,\s*([^,)]+?)\s*\)`)
	dateAddPattern           = regexp.MustCompile(`(?i)\bDATEADD\s*\(\s*(year|yy|yyyy|quarter|qq|q|month|mm|m|dayofyear|dy|y|day|dd|d|week|wk|ww|hour|hh|minute|mi|n|second|ss|s|millisecond|ms)\s*,\s*([^,]+?)\s*,\s*([^)]+?)\s*\)`)
	dateDiffPattern          = regexp.MustCompile(`(?i)\bDATEDIFF\s*\(\s*(year|yy|yyyy|quarter|qq|q|month|mm|m|dayofyear|dy|y|day|dd|d|week|wk|ww|hour|hh|minute|mi|n|second|ss|s|millisecond|ms)\s*,\s*([^,]+?)\s*,\s*([^)]+?)\s*\)`)
	iifPattern               = regexp.MustCompile(`(?i)\bIIF\s*\(\s*([^,]+?)\s*,\s*([^,]+?)\s*,\s*([^)]+?)\s*\)`)
	stuffPattern             = regexp.MustCompile(`(?i)\bSTUFF\s*\(\s*([^,]+?)\s*,\s*(\d+)\s*,\s*(\d+)\s*,\s*([^)]+?)\s*\)`)
	collatePattern           = regexp.MustCompile(`(?i)\bCOLLATE\s+\w+`)
	replicatePattern         = regexp.MustCompile(`(?i)\bREPLICATE\s*\(`)
	spacePattern             = regexp.MustCompile(`(?i)\bSPACE\s*\(\s*([^)]+?)\s*\)`)
	convertSafePattern       = regexp.MustCompile(`(?i)\bCONVERT\s*\(\s*(varchar|nvarchar|nchar|char|text|ntext|uniqueidentifier|sysname|money|smallmoney|integer|int|bigint|smallint|tinyint|float|real|bit|date|datetime2|smalldatetime|datetime|datetimeoffset|numeric|decimal)\s*(?:\(\s*[\d,\s]+\s*\))?\s*,\s*([^,)]+?)\s*\)`)
	logSingleArgPattern      = regexp.MustCompile(`(?i)\bLOG\s*\(\s*([^,)]+?)\s*\)`)
	castTypePattern          = regexp.MustCompile(`(?i)\bCAST\s*\(\s*(.+?)\s+AS\s+(datetimeoffset|datetime2|smalldatetime|datetime|smallmoney|money|uniqueidentifier|sysname|nvarchar|nchar|ntext|tinyint|bit)\s*(?:\([^)]*\))?\s*\)`)
	schemaBindingPattern     = regexp.MustCompile(`(?i)\bWITH\s+SCHEMABINDING\b`)
	sqlFunctionPattern       = regexp.MustCompile(`(?i)\b([A-Z_][A-Z0-9_]*)\(`)
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
			for _, defaultConstraint := range table.DefaultConstraints {
				if defaultConstraint.Definition == "gen_random_uuid()" {
					needsPGCrypto = true
					break
				}
			}
		}
	}

	if needsPGCrypto {
		var versionNum int
		if err := target.pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&versionNum); err != nil {
			return fmt.Errorf("check postgresql version: %w", err)
		}
		if versionNum < 130000 {
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

func (target *Target) ResetSequences(ctx context.Context, database *catalog.Database) error {
	for _, schema := range database.SortedSchemas() {
		for _, table := range schema.SortedTables() {
			for _, column := range table.Columns {
				if !column.Identity {
					continue
				}
				var maxVal int64
				query := fmt.Sprintf(
					`SELECT COALESCE(MAX(%s), 0) FROM %s.%s`,
					quoteIdentifier(column.Name),
					quoteIdentifier(table.Schema),
					quoteIdentifier(table.Name),
				)
				if err := target.pool.QueryRow(ctx, query).Scan(&maxVal); err != nil {
					return fmt.Errorf("get max identity %s.%s.%s: %w", table.Schema, table.Name, column.Name, err)
				}
				increment := column.IdentityIncrement
				if increment <= 0 {
					increment = 1
				}
				nextVal := max(maxVal+increment, column.IdentitySeed)
				stmt := fmt.Sprintf(
					`ALTER TABLE %s.%s ALTER COLUMN %s RESTART WITH %d`,
					quoteIdentifier(table.Schema),
					quoteIdentifier(table.Name),
					quoteIdentifier(column.Name),
					nextVal,
				)
				if _, err := target.pool.Exec(ctx, stmt); err != nil {
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
	predicate, ok := normalizeIndexPredicate(index.Predicate)
	if !ok {
		return ""
	}

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

	return fmt.Sprintf(
		"ALTER TABLE %s.%s ADD CONSTRAINT %s UNIQUE (%s)",
		quoteIdentifier(table.Schema),
		quoteIdentifier(table.Name),
		quoteIdentifier(uniqueConstraint.Name),
		strings.Join(columns, ", "),
	), nil
}

func renderCreateCheckConstraint(table *catalog.Table, checkConstraint *catalog.CheckConstraint) (string, error) {
	definition := normalizeSQLExpression(checkConstraint.Definition)
	if err := validateCheckConstraintDefinition(definition); err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"ALTER TABLE %s.%s ADD CONSTRAINT %s CHECK (%s)",
		quoteIdentifier(table.Schema),
		quoteIdentifier(table.Name),
		quoteIdentifier(checkConstraint.Name),
		definition,
	), nil
}

func renderCreateDefaultConstraint(table *catalog.Table, defaultConstraint *catalog.DefaultConstraint) (string, error) {
	definition := normalizeSQLExpression(defaultConstraint.Definition)
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
	upper := strings.ToUpper(sqlForValidation(definition))
	unsupported := []string{
		"TOP ",
		"TOP(",
		"WITH ENCRYPTION",
		"WITH CHECK OPTION",
		"CROSS APPLY",
		"OUTER APPLY",
		"TRY_CONVERT(",
		"TRY_CAST(",
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
	normalized = schemaBindingPattern.ReplaceAllString(normalized, "")
	normalized = bracketIdentifierPattern.ReplaceAllString(normalized, `"$1"`)
	normalized = normalizeSQLExpression(normalized)
	return normalized
}

func normalizeSQLExpression(expression string) string {
	normalized := expression
	normalized = bracketIdentifierPattern.ReplaceAllString(normalized, `"$1"`)
	normalized = collatePattern.ReplaceAllString(normalized, "")
	normalized = unicodeStringPattern.ReplaceAllString(normalized, `'`)
	normalized = isNullPattern.ReplaceAllString(normalized, "COALESCE(")
	normalized = getDatePattern.ReplaceAllString(normalized, "CURRENT_TIMESTAMP")
	normalized = getUTCDatePattern.ReplaceAllString(normalized, "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')")
	normalized = newIDPattern.ReplaceAllString(normalized, "gen_random_uuid()")
	normalized = lenPattern.ReplaceAllString(normalized, "LENGTH(")
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
	return normalized
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
	return fmt.Sprintf("(%s + (%s * INTERVAL '%s'))", expr, n, interval)
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
		return fmt.Sprintf("(((%s)::date - (%s)::date) / 7)", end, start)
	case "hour", "hh":
		return fmt.Sprintf("(EXTRACT(EPOCH FROM (%s)::timestamp - (%s)::timestamp) / 3600)::bigint", end, start)
	case "minute", "mi", "n":
		return fmt.Sprintf("(EXTRACT(EPOCH FROM (%s)::timestamp - (%s)::timestamp) / 60)::bigint", end, start)
	case "second", "ss", "s":
		return fmt.Sprintf("(EXTRACT(EPOCH FROM (%s)::timestamp - (%s)::timestamp))::bigint", end, start)
	case "millisecond", "ms":
		return fmt.Sprintf("(EXTRACT(EPOCH FROM (%s)::timestamp - (%s)::timestamp) * 1000)::bigint", end, start)
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

	allowedFunctions := map[string]struct{}{
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
		"UPPER":           {},
	}

	for _, match := range sqlFunctionPattern.FindAllStringSubmatch(upper, -1) {
		functionName := match[1]
		if _, ok := allowedFunctions[functionName]; ok {
			continue
		}
		return false
	}

	return true
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

	allowedFunctions := map[string]struct{}{
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
		"UPPER":           {},
	}

	for _, match := range sqlFunctionPattern.FindAllStringSubmatch(upper, -1) {
		functionName := match[1]
		if _, ok := allowedFunctions[functionName]; ok {
			continue
		}
		return false
	}

	return true
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

	allowedFunctions := map[string]struct{}{
		"ABS":               {},
		"CASE":              {},
		"CAST":              {},
		"CEILING":           {},
		"COALESCE":          {},
		"CONCAT":            {},
		"CURRENT_TIMESTAMP": {},
		"EXP":               {},
		"EXTRACT":           {},
		"FLOOR":             {},
		"GEN_RANDOM_UUID":   {},
		"LEFT":              {},
		"LENGTH":            {},
		"LN":                {},
		"LOWER":             {},
		"LTRIM":             {},
		"NULLIF":            {},
		"OCTET_LENGTH":      {},
		"OVERLAY":           {},
		"PI":                {},
		"POSITION":          {},
		"POWER":             {},
		"REPEAT":            {},
		"RIGHT":             {},
		"ROUND":             {},
		"RTRIM":             {},
		"SQRT":              {},
		"SUBSTRING":         {},
		"TRIM":              {},
		"UPPER":             {},
	}

	for _, match := range sqlFunctionPattern.FindAllStringSubmatch(upper, -1) {
		functionName := match[1]
		if _, ok := allowedFunctions[functionName]; ok {
			continue
		}
		return false
	}

	return true
}

func sqlForValidation(sql string) string {
	var builder strings.Builder
	builder.Grow(len(sql))

	inString := false
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		if ch != '\'' {
			if inString {
				builder.WriteByte(' ')
			} else {
				builder.WriteByte(ch)
			}
			continue
		}

		builder.WriteByte(' ')
		if inString && i+1 < len(sql) && sql[i+1] == '\'' {
			builder.WriteByte(' ')
			i++
			continue
		}
		inString = !inString
	}

	return builder.String()
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
