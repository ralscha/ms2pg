# ms2pg - MSSQL to PostgreSQL migration tool

ms2pg is a focused MSSQL to PostgreSQL migration tool written in Go. It introspects a SQL Server database, maps supported objects to PostgreSQL, creates the target structure, copies table data with PostgreSQL COPY, and recreates supported views and indexes.

The current implementation is built for schema-and-data migration of relational objects that are common in application databases. It is intentionally strict: when it encounters unsupported source definitions, it fails with a clear error instead of silently producing a partial or incorrect translation.

## What it migrates

- schemas
- tables and columns
- primary keys
- named unique constraints
- secondary indexes
- PostgreSQL-safe filtered indexes
- included-column indexes
- named default constraints, recreated as PostgreSQL column defaults
- supported check constraints
- foreign keys discovered through INFORMATION_SCHEMA
- views that match the supported normalization rules
- table data using streaming PostgreSQL COPY
- identity-backed sequences reset after data load

## How it works

For a normal migration, ms2pg performs these steps:

1. connect to the source SQL Server database and the target PostgreSQL database
2. introspect the source catalog, with optional schema and table filters
3. map supported MSSQL column types to PostgreSQL types
4. create schemas and tables in PostgreSQL
5. create supported default constraints as PostgreSQL column defaults
6. copy table data into PostgreSQL with COPY
7. reset identity sequences after data load
8. create indexes, unique constraints, check constraints, and foreign keys
9. create views after base tables are available

When `-schema-only` is enabled, object creation still runs, but table rows are not copied and sequence reset is skipped.

## Supported normalization

View, default-expression, and check-constraint translation is selective and based on the rules currently implemented in the codebase and integration tests. Supported rewrites include:

- bracketed identifiers
- `SET ANSI_NULLS`
- `SET QUOTED_IDENTIFIER`
- `ISNULL`
- `N'...'` string literals
- `GETDATE()`
- `GETUTCDATE()`
- `NEWID()`
- `LEN()`
- `DATALENGTH()`
- `CHARINDEX()`
- `DATEADD(day, n, ...)`
- `DATEDIFF(day, start, end)`

If a source definition falls outside the supported translation rules, ms2pg returns an explicit error describing the unsupported token or expression.

## Filtering

The migrator supports selective runs with:

- `-include-schemas`
- `-include-tables`
- `-exclude-schemas`
- `-exclude-tables`

Each filter accepts a comma-separated list of glob patterns. Table filters can be unqualified, such as `users`, or schema-qualified, such as `reporting.user_names` or `sales.*`. Exclude filters are applied after include filters.

Foreign keys that point to tables outside the selected migration set are skipped automatically so filtered runs still produce a valid target schema.

## Requirements

- Go 1.26+
- access to a SQL Server source database
- access to a PostgreSQL target database
- Docker, if you want to run the container-backed integration tests

## Usage

Basic migration:

```sh
go run . \
  -source 'sqlserver://sa:yourStrong(!)Password@localhost:1433?database=source_db&encrypt=disable' \
  -target 'postgres://postgres:postgres@localhost:5432/target_db?sslmode=disable'
```

Filtered migration:

```sh
go run . \
  -source 'sqlserver://sa:yourStrong(!)Password@localhost:1433?database=source_db&encrypt=disable' \
  -target 'postgres://postgres:postgres@localhost:5432/target_db?sslmode=disable' \
  -include-schemas 'dbo,reporting' \
  -include-tables 'users,reporting.user_names,reporting.user_labels,reporting.user_metrics' \
  -exclude-tables 'reporting.legacy_*'
```

Schema-only migration:

```sh
go run . \
  -source 'sqlserver://sa:yourStrong(!)Password@localhost:1433?database=source_db&encrypt=disable' \
  -target 'postgres://postgres:postgres@localhost:5432/target_db?sslmode=disable' \
  -schema-only
```

Verbose logging:

```sh
go run . \
  -source '...' \
  -target '...' \
  -verbose
```

## Flags

- `-source`: MSSQL connection string
- `-target`: PostgreSQL connection string
- `-schema-only`: create schemas, tables, constraints, indexes, and views without copying table rows
- `-verbose`: enable debug logging
- `-include-schemas`: comma-separated schema filters with glob support
- `-include-tables`: comma-separated table and view filters with glob support
- `-exclude-schemas`: comma-separated schema filters to skip
- `-exclude-tables`: comma-separated table and view filters to skip

Both `-source` and `-target` are required.


## License

This project is licensed under the MIT License. See [LICENSE](LICENSE).
