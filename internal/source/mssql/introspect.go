package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	"ms2pg/internal/catalog"
)

const tablesQuery = `
SELECT TABLE_SCHEMA, TABLE_NAME
FROM INFORMATION_SCHEMA.TABLES
WHERE TABLE_TYPE = 'BASE TABLE'
ORDER BY TABLE_SCHEMA, TABLE_NAME`

const columnsQuery = `
SELECT
	s.name,
	t.name,
	c.name,
	c.column_id,
	COALESCE(bt.name, tp.name) AS base_type,
	c.max_length,
	c.precision,
	c.scale,
	c.is_nullable,
	COALESCE(dc.definition, ''),
	c.is_identity,
	c.is_computed
FROM sys.columns c
JOIN sys.tables t ON t.object_id = c.object_id
JOIN sys.schemas s ON s.schema_id = t.schema_id
JOIN sys.types tp ON tp.user_type_id = c.user_type_id
LEFT JOIN sys.types bt ON bt.user_type_id = tp.system_type_id AND bt.is_user_defined = 0
LEFT JOIN sys.default_constraints dc
	ON dc.parent_object_id = c.object_id AND dc.parent_column_id = c.column_id
WHERE t.is_ms_shipped = 0
ORDER BY s.name, t.name, c.column_id`

const primaryKeysQuery = `
SELECT
	tc.TABLE_SCHEMA,
	tc.TABLE_NAME,
	kcu.COLUMN_NAME,
	kcu.ORDINAL_POSITION
FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
	ON tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME
	AND tc.TABLE_SCHEMA = kcu.TABLE_SCHEMA
	AND tc.TABLE_NAME = kcu.TABLE_NAME
WHERE tc.CONSTRAINT_TYPE = 'PRIMARY KEY'
ORDER BY tc.TABLE_SCHEMA, tc.TABLE_NAME, kcu.ORDINAL_POSITION`

const uniqueConstraintsQuery = `
SELECT
	tc.TABLE_SCHEMA,
	tc.TABLE_NAME,
	tc.CONSTRAINT_NAME,
	kcu.COLUMN_NAME,
	kcu.ORDINAL_POSITION
FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
	ON tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME
	AND tc.TABLE_SCHEMA = kcu.TABLE_SCHEMA
	AND tc.TABLE_NAME = kcu.TABLE_NAME
WHERE tc.CONSTRAINT_TYPE = 'UNIQUE'
ORDER BY tc.TABLE_SCHEMA, tc.TABLE_NAME, tc.CONSTRAINT_NAME, kcu.ORDINAL_POSITION`

const indexesQuery = `
SELECT
	s.name,
	o.name,
	i.name,
	c.name,
	i.is_unique,
	i.is_primary_key,
	COALESCE(i.filter_definition, ''),
	ic.key_ordinal,
	ic.is_included_column,
	ic.index_column_id,
	ic.is_descending_key
FROM sys.indexes i
JOIN sys.objects o ON i.object_id = o.object_id
JOIN sys.schemas s ON o.schema_id = s.schema_id
JOIN sys.index_columns ic ON ic.object_id = i.object_id
	AND ic.index_id = i.index_id
JOIN sys.columns c ON c.object_id = i.object_id
	AND c.column_id = ic.column_id
WHERE o.type = 'U'
	AND i.name IS NOT NULL
	AND i.is_primary_key = 0
	AND i.is_unique_constraint = 0
ORDER BY s.name, o.name, i.name, ic.is_included_column, ic.key_ordinal, ic.index_column_id`

const foreignKeysQuery = `
SELECT
	REPLACE(kcu1.CONSTRAINT_NAME, '.', '_'),
	kcu1.TABLE_SCHEMA,
	kcu1.TABLE_NAME,
	kcu1.COLUMN_NAME,
	kcu2.TABLE_SCHEMA,
	kcu2.TABLE_NAME,
	kcu2.COLUMN_NAME,
	rc.UPDATE_RULE,
	rc.DELETE_RULE,
	kcu1.ORDINAL_POSITION
FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS rc
JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu1
	ON kcu1.CONSTRAINT_CATALOG = rc.CONSTRAINT_CATALOG
	AND kcu1.CONSTRAINT_SCHEMA = rc.CONSTRAINT_SCHEMA
	AND kcu1.CONSTRAINT_NAME = rc.CONSTRAINT_NAME
JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu2
	ON kcu2.CONSTRAINT_CATALOG = rc.UNIQUE_CONSTRAINT_CATALOG
	AND kcu2.CONSTRAINT_SCHEMA = rc.UNIQUE_CONSTRAINT_SCHEMA
	AND kcu2.CONSTRAINT_NAME = rc.UNIQUE_CONSTRAINT_NAME
WHERE kcu1.ORDINAL_POSITION = kcu2.ORDINAL_POSITION
ORDER BY kcu1.CONSTRAINT_NAME, kcu1.ORDINAL_POSITION`

const checkConstraintsQuery = `
SELECT
	s.name,
	t.name,
	cc.name,
	cc.definition
FROM sys.check_constraints cc
JOIN sys.tables t ON t.object_id = cc.parent_object_id
JOIN sys.schemas s ON s.schema_id = t.schema_id
ORDER BY s.name, t.name, cc.name`

const defaultConstraintsQuery = `
SELECT
	s.name,
	t.name,
	dc.name,
	c.name,
	dc.definition
FROM sys.default_constraints dc
JOIN sys.tables t ON t.object_id = dc.parent_object_id
JOIN sys.schemas s ON s.schema_id = t.schema_id
JOIN sys.columns c ON c.object_id = dc.parent_object_id
	AND c.column_id = dc.parent_column_id
ORDER BY s.name, t.name, c.column_id`

const identityColumnsQuery = `
SELECT
	s.name,
	t.name,
	c.name,
	CAST(ic.seed_value AS bigint),
	CAST(ic.increment_value AS bigint)
FROM sys.identity_columns ic
JOIN sys.columns c ON c.object_id = ic.object_id
	AND c.column_id = ic.column_id
JOIN sys.tables t ON t.object_id = ic.object_id
JOIN sys.schemas s ON s.schema_id = t.schema_id
ORDER BY s.name, t.name, c.name`

const viewsQuery = `
SELECT s.name, v.name, m.definition
FROM sys.views v
JOIN sys.schemas s ON s.schema_id = v.schema_id
JOIN sys.sql_modules m ON m.object_id = v.object_id
ORDER BY s.name, v.name`

type Source struct {
	db *sql.DB
}

func Open(connectionString string) (*Source, error) {
	db, err := sql.Open("sqlserver", connectionString)
	if err != nil {
		return nil, err
	}
	return &Source{db: db}, nil
}

func (source *Source) Close() error {
	return source.db.Close()
}

func (source *Source) Ping(ctx context.Context) error {
	return source.db.PingContext(ctx)
}

func (source *Source) Introspect(ctx context.Context, filters catalog.Filters) (*catalog.Database, error) {
	database := &catalog.Database{}
	schemas := make(map[string]*catalog.Schema)
	tables := make(map[string]*catalog.Table)

	if err := source.loadTables(ctx, filters, schemas, tables); err != nil {
		return nil, err
	}
	if err := source.loadColumns(ctx, tables); err != nil {
		return nil, err
	}
	if err := source.loadIdentityColumns(ctx, tables); err != nil {
		return nil, err
	}
	if err := source.loadPrimaryKeys(ctx, tables); err != nil {
		return nil, err
	}
	if err := source.loadUniqueConstraints(ctx, tables); err != nil {
		return nil, err
	}
	if err := source.loadIndexes(ctx, tables); err != nil {
		return nil, err
	}
	if err := source.loadDefaultConstraints(ctx, tables); err != nil {
		return nil, err
	}
	if err := source.loadForeignKeys(ctx, tables); err != nil {
		return nil, err
	}
	if err := source.loadCheckConstraints(ctx, tables); err != nil {
		return nil, err
	}
	if err := source.loadViews(ctx, filters, schemas); err != nil {
		return nil, err
	}

	for _, schema := range schemas {
		database.Schemas = append(database.Schemas, schema)
	}
	sort.Slice(database.Schemas, func(i, j int) bool {
		return database.Schemas[i].Name < database.Schemas[j].Name
	})

	return database, nil
}

func (source *Source) StreamTable(ctx context.Context, table *catalog.Table, handleRow func([]any) error) error {
	selectList := buildSelectList(table)
	qualifiedName := quoteQualified(table.Schema, table.Name)

	var queryBuilder strings.Builder
	queryBuilder.Grow(len("SELECT ") + len(selectList) + len(" FROM ") + len(qualifiedName))
	queryBuilder.WriteString("SELECT ")
	queryBuilder.WriteString(selectList)
	queryBuilder.WriteString(" FROM ")
	queryBuilder.WriteString(qualifiedName)

	query := queryBuilder.String()
	rows, err := source.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("query %s.%s: %w", table.Schema, table.Name, err)
	}
	defer func() {
		_ = rows.Close()
	}()

	values := make([]any, len(table.Columns))
	scanArgs := make([]any, len(table.Columns))
	for index := range values {
		scanArgs[index] = &values[index]
	}

	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			return fmt.Errorf("scan %s.%s: %w", table.Schema, table.Name, err)
		}
		row := make([]any, len(values))
		for index, value := range values {
			row[index] = normalizeValue(table.Columns[index], value)
		}
		if err := handleRow(row); err != nil {
			return err
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s.%s: %w", table.Schema, table.Name, err)
	}

	return nil
}

func (source *Source) loadTables(ctx context.Context, filters catalog.Filters, schemas map[string]*catalog.Schema, tables map[string]*catalog.Table) error {
	rows, err := source.db.QueryContext(ctx, tablesQuery)
	if err != nil {
		return fmt.Errorf("query tables: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var schemaName string
		var tableName string
		if err := rows.Scan(&schemaName, &tableName); err != nil {
			return fmt.Errorf("scan table metadata: %w", err)
		}
		if !filters.MatchesTable(schemaName, tableName) {
			continue
		}
		schema := schemas[schemaName]
		if schema == nil {
			schema = &catalog.Schema{Name: schemaName}
			schemas[schemaName] = schema
		}
		table := &catalog.Table{Schema: schemaName, Name: tableName}
		schema.Tables = append(schema.Tables, table)
		tables[tableKey(schemaName, tableName)] = table
	}

	return rows.Err()
}

func (source *Source) loadColumns(ctx context.Context, tables map[string]*catalog.Table) error {
	rows, err := source.db.QueryContext(ctx, columnsQuery)
	if err != nil {
		return fmt.Errorf("query columns: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var schemaName string
		var tableName string
		var column catalog.Column
		var nullable bool
		var identity bool
		var computed bool
		if err := rows.Scan(
			&schemaName,
			&tableName,
			&column.Name,
			&column.Ordinal,
			&column.SourceType,
			&column.Length,
			&column.Precision,
			&column.Scale,
			&nullable,
			&column.Default,
			&identity,
			&computed,
		); err != nil {
			return fmt.Errorf("scan column metadata: %w", err)
		}

		table := tables[tableKey(schemaName, tableName)]
		if table == nil {
			continue
		}

		column.Nullable = nullable
		column.Identity = identity
		column.Computed = computed
		if column.Identity {
			column.IdentitySeed = 1
			column.IdentityIncrement = 1
		}
		table.Columns = append(table.Columns, &column)
	}

	return rows.Err()
}

func (source *Source) loadPrimaryKeys(ctx context.Context, tables map[string]*catalog.Table) error {
	rows, err := source.db.QueryContext(ctx, primaryKeysQuery)
	if err != nil {
		return fmt.Errorf("query primary keys: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var schemaName string
		var tableName string
		var columnName string
		var ordinal int
		if err := rows.Scan(&schemaName, &tableName, &columnName, &ordinal); err != nil {
			return fmt.Errorf("scan primary key metadata: %w", err)
		}
		table := tables[tableKey(schemaName, tableName)]
		if table == nil {
			continue
		}
		table.PrimaryKey = append(table.PrimaryKey, columnName)
	}

	return rows.Err()
}

func (source *Source) loadViews(ctx context.Context, filters catalog.Filters, schemas map[string]*catalog.Schema) error {
	rows, err := source.db.QueryContext(ctx, viewsQuery)
	if err != nil {
		return fmt.Errorf("query views: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var schemaName string
		var viewName string
		var definition string
		if err := rows.Scan(&schemaName, &viewName, &definition); err != nil {
			return fmt.Errorf("scan view metadata: %w", err)
		}
		if !filters.MatchesView(schemaName, viewName) {
			continue
		}
		schema := schemas[schemaName]
		if schema == nil {
			schema = &catalog.Schema{Name: schemaName}
			schemas[schemaName] = schema
		}
		schema.Views = append(schema.Views, &catalog.View{Schema: schemaName, Name: viewName, Definition: definition})
	}

	return rows.Err()
}

func (source *Source) loadIndexes(ctx context.Context, tables map[string]*catalog.Table) error {
	rows, err := source.db.QueryContext(ctx, indexesQuery)
	if err != nil {
		return fmt.Errorf("query indexes: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	indexMap := make(map[string]*catalog.Index)

	for rows.Next() {
		var schemaName string
		var tableName string
		var indexName string
		var columnName string
		var unique bool
		var primaryKey bool
		var filterDefinition string
		var keyOrdinal int
		var included bool
		var indexColumnID int
		var descending bool
		if err := rows.Scan(
			&schemaName,
			&tableName,
			&indexName,
			&columnName,
			&unique,
			&primaryKey,
			&filterDefinition,
			&keyOrdinal,
			&included,
			&indexColumnID,
			&descending,
		); err != nil {
			return fmt.Errorf("scan index metadata: %w", err)
		}
		if primaryKey {
			continue
		}
		_ = indexColumnID

		table := tables[tableKey(schemaName, tableName)]
		if table == nil {
			continue
		}

		mapKey := tableKey(schemaName, tableName) + "." + indexName
		index := indexMap[mapKey]
		if index == nil {
			index = &catalog.Index{Name: indexName, Unique: unique, Predicate: filterDefinition}
			indexMap[mapKey] = index
			table.Indexes = append(table.Indexes, index)
		}
		if included {
			index.IncludedColumns = append(index.IncludedColumns, columnName)
			continue
		}
		if keyOrdinal == 0 {
			continue
		}
		index.Columns = append(index.Columns, columnName)
		index.DescendingColumns = append(index.DescendingColumns, descending)
	}

	return rows.Err()
}

func (source *Source) loadIdentityColumns(ctx context.Context, tables map[string]*catalog.Table) error {
	rows, err := source.db.QueryContext(ctx, identityColumnsQuery)
	if err != nil {
		return fmt.Errorf("query identity columns: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var schemaName string
		var tableName string
		var columnName string
		var seed int64
		var increment int64
		if err := rows.Scan(&schemaName, &tableName, &columnName, &seed, &increment); err != nil {
			return fmt.Errorf("scan identity column metadata: %w", err)
		}

		table := tables[tableKey(schemaName, tableName)]
		if table == nil {
			continue
		}

		for _, column := range table.Columns {
			if strings.EqualFold(column.Name, columnName) && column.Identity {
				column.IdentitySeed = seed
				column.IdentityIncrement = increment
				break
			}
		}
	}

	return rows.Err()
}

func (source *Source) loadUniqueConstraints(ctx context.Context, tables map[string]*catalog.Table) error {
	rows, err := source.db.QueryContext(ctx, uniqueConstraintsQuery)
	if err != nil {
		return fmt.Errorf("query unique constraints: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	constraintMap := make(map[string]*catalog.UniqueConstraint)

	for rows.Next() {
		var schemaName string
		var tableName string
		var constraintName string
		var columnName string
		var ordinal int
		if err := rows.Scan(&schemaName, &tableName, &constraintName, &columnName, &ordinal); err != nil {
			return fmt.Errorf("scan unique constraint metadata: %w", err)
		}
		_ = ordinal

		table := tables[tableKey(schemaName, tableName)]
		if table == nil {
			continue
		}

		mapKey := tableKey(schemaName, tableName) + "." + constraintName
		constraint := constraintMap[mapKey]
		if constraint == nil {
			constraint = &catalog.UniqueConstraint{Name: constraintName}
			constraintMap[mapKey] = constraint
			table.UniqueConstraints = append(table.UniqueConstraints, constraint)
		}
		constraint.Columns = append(constraint.Columns, columnName)
	}

	return rows.Err()
}

func (source *Source) loadForeignKeys(ctx context.Context, tables map[string]*catalog.Table) error {
	rows, err := source.db.QueryContext(ctx, foreignKeysQuery)
	if err != nil {
		return fmt.Errorf("query foreign keys: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	foreignKeyMap := make(map[string]*catalog.ForeignKey)

	for rows.Next() {
		var name string
		var schemaName string
		var tableName string
		var columnName string
		var referencedSchema string
		var referencedTable string
		var referencedColumn string
		var updateRule string
		var deleteRule string
		var ordinal int
		if err := rows.Scan(
			&name,
			&schemaName,
			&tableName,
			&columnName,
			&referencedSchema,
			&referencedTable,
			&referencedColumn,
			&updateRule,
			&deleteRule,
			&ordinal,
		); err != nil {
			return fmt.Errorf("scan foreign key metadata: %w", err)
		}

		table := tables[tableKey(schemaName, tableName)]
		if table == nil {
			continue
		}

		mapKey := tableKey(schemaName, tableName) + "." + name
		foreignKey := foreignKeyMap[mapKey]
		if foreignKey == nil {
			foreignKey = &catalog.ForeignKey{
				Name:             name,
				ReferencedSchema: referencedSchema,
				ReferencedTable:  referencedTable,
				UpdateRule:       updateRule,
				DeleteRule:       deleteRule,
			}
			foreignKeyMap[mapKey] = foreignKey
			table.ForeignKeys = append(table.ForeignKeys, foreignKey)
		}
		foreignKey.Columns = append(foreignKey.Columns, columnName)
		foreignKey.ReferencedColumns = append(foreignKey.ReferencedColumns, referencedColumn)
	}

	return rows.Err()
}

func (source *Source) loadCheckConstraints(ctx context.Context, tables map[string]*catalog.Table) error {
	rows, err := source.db.QueryContext(ctx, checkConstraintsQuery)
	if err != nil {
		return fmt.Errorf("query check constraints: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var schemaName string
		var tableName string
		var name string
		var definition string
		if err := rows.Scan(&schemaName, &tableName, &name, &definition); err != nil {
			return fmt.Errorf("scan check constraint metadata: %w", err)
		}

		table := tables[tableKey(schemaName, tableName)]
		if table == nil {
			continue
		}

		table.CheckConstraints = append(table.CheckConstraints, &catalog.CheckConstraint{
			Name:       name,
			Definition: definition,
		})
	}

	return rows.Err()
}

func (source *Source) loadDefaultConstraints(ctx context.Context, tables map[string]*catalog.Table) error {
	rows, err := source.db.QueryContext(ctx, defaultConstraintsQuery)
	if err != nil {
		return fmt.Errorf("query default constraints: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var schemaName string
		var tableName string
		var name string
		var columnName string
		var definition string
		if err := rows.Scan(&schemaName, &tableName, &name, &columnName, &definition); err != nil {
			return fmt.Errorf("scan default constraint metadata: %w", err)
		}

		table := tables[tableKey(schemaName, tableName)]
		if table == nil {
			continue
		}

		table.DefaultConstraints = append(table.DefaultConstraints, &catalog.DefaultConstraint{
			Name:       name,
			Column:     columnName,
			Definition: definition,
		})
	}

	return rows.Err()
}

func buildSelectList(table *catalog.Table) string {
	selects := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		selects = append(selects, selectExpression(column))
	}
	return strings.Join(selects, ", ")
}

func selectExpression(column *catalog.Column) string {
	name := quoteIdentifier(column.Name)
	var expression string
	switch strings.ToLower(column.SourceType) {
	case "datetimeoffset":
		expression = fmt.Sprintf("CONVERT(varchar(35), %s, 127)", name)
	case "datetime", "datetime2", "smalldatetime":
		expression = fmt.Sprintf("CONVERT(varchar(30), %s, 126)", name)
	case "time":
		expression = fmt.Sprintf("CONVERT(varchar(30), %s, 114)", name)
	case "money", "smallmoney":
		// Cast to decimal so the driver returns an exact decimal string rather
		// than a float64, preventing floating-point rounding errors.
		expression = fmt.Sprintf("CAST(%s AS decimal(19,4))", name)
	case "sql_variant":
		// sql_variant can hold any type; cast to nvarchar so the driver always
		// returns a string, which pgx can reliably copy to a text column.
		expression = fmt.Sprintf("CAST(%s AS nvarchar(max))", name)
	default:
		expression = name
	}
	return expression + " AS " + name
}

func normalizeValue(column *catalog.Column, value any) any {
	switch typed := value.(type) {
	case []byte:
		if normalized, ok := normalizeTemporalValue(column, string(typed)); ok {
			return normalized
		}
		clone := make([]byte, len(typed))
		copy(clone, typed)
		return clone
	case string:
		if normalized, ok := normalizeTemporalValue(column, typed); ok {
			return normalized
		}
		return typed
	default:
		return value
	}
}

func normalizeTemporalValue(column *catalog.Column, value string) (any, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, false
	}

	switch column.TargetType {
	case "timestamp":
		for _, layout := range []string{
			"2006-01-02T15:04:05.9999999",
			"2006-01-02T15:04:05.999999",
			"2006-01-02T15:04:05.999",
			"2006-01-02T15:04:05",
		} {
			parsed, err := time.Parse(layout, trimmed)
			if err == nil {
				return parsed, true
			}
		}
	case "timestamptz":
		for _, layout := range []string{
			"2006-01-02T15:04:05.9999999Z07:00",
			"2006-01-02T15:04:05.999999Z07:00",
			"2006-01-02T15:04:05.999Z07:00",
			"2006-01-02T15:04:05Z07:00",
		} {
			parsed, err := time.Parse(layout, trimmed)
			if err == nil {
				return parsed, true
			}
		}
	case "date":
		parsed, err := time.Parse("2006-01-02", trimmed)
		if err == nil {
			return parsed, true
		}
	case "time":
		normalized := trimmed
		lastColon := strings.LastIndex(normalized, ":")
		if lastColon > len("15:04:05")-1 {
			normalized = normalized[:lastColon] + "." + normalized[lastColon+1:]
		}
		for _, layout := range []string{
			"15:04:05.9999999",
			"15:04:05.999999",
			"15:04:05.999",
			"15:04:05",
		} {
			parsed, err := time.Parse(layout, normalized)
			if err == nil {
				return parsed.Format("15:04:05.999999"), true
			}
		}
	}

	return nil, false
}

func tableKey(schemaName, tableName string) string {
	return schemaName + "." + tableName
}

func quoteQualified(schemaName, objectName string) string {
	return quoteIdentifier(schemaName) + "." + quoteIdentifier(objectName)
}

func quoteIdentifier(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}
