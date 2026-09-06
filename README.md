# ms2pg - MSSQL to PostgreSQL migration tool

ms2pg is a focused Microsoft SQL Server to PostgreSQL migration tool written in Go. It introspects a SQL Server database, maps supported objects to PostgreSQL, creates the target structure, copies table data with PostgreSQL COPY, and recreates supported views and indexes.

The current implementation targets common relational application schemas. It validates known SQL translation limits before changing the target and fails clearly when a definition cannot be translated safely. SQL Server and PostgreSQL are not feature-equivalent, so the supported boundary and lossy mappings are documented below.

## What it migrates

- schemas
- tables and columns
- named primary keys
- named unique constraints, including SQL Server's nullable-unique behavior
- secondary indexes
- PostgreSQL-safe filtered indexes
- included-column indexes
- named default constraints, recreated as PostgreSQL column defaults
- supported check constraints, preserving untrusted state with PostgreSQL `NOT VALID`
- foreign keys discovered through SQL Server system catalogs, including keys backed by unique indexes and untrusted state
- views that match the supported normalization rules
- table data using streaming PostgreSQL COPY
- identity columns with their integer width, seed, increment, and post-load sequence state preserved

The selected target tables and views must not already exist. This prevents a repeated run from appending duplicate data or overwriting an existing relation.

## Data type behavior

| SQL Server | PostgreSQL | Notes |
| --- | --- | --- |
| `bigint`, `int`, `smallint`, `tinyint` | `bigint`, `integer`, `smallint`, `smallint` | Identity width, bounds, seed, and positive or negative increment are preserved. |
| `decimal`, `numeric` | `numeric(p,s)` | Precision and scale are preserved. Decimal/numeric identity columns are rejected because PostgreSQL identity sequences cannot preserve their full range. |
| `float`, `real` | `double precision`/`real` | SQL Server `float(1..24)` maps to `real`. |
| `money`, `smallmoney` | `numeric(19,4)`/`numeric(10,4)` | Exact decimal values are copied. |
| `char`, `varchar`, `nchar`, `nvarchar` | `character(n)`/`character varying(n)` | Declared limits are preserved; `max`, `text`, and `ntext` map to `text`. |
| binary types, `image`, `rowversion` | `bytea` | `rowversion` values are snapshots; PostgreSQL does not recreate SQL Server's database-wide rowversion generator. |
| `uniqueidentifier` | `uuid` | `NEWID()`/`NEWSEQUENTIALID()` defaults use `gen_random_uuid()`; sequential UUID generation is not preserved. |
| date/time types | corresponding PostgreSQL date/time type | Fractional precision is preserved up to PostgreSQL's six-digit limit. `datetimeoffset` maps to `timestamptz`. |
| `xml` | `xml` | XML data is copied to PostgreSQL's XML type. |
| `hierarchyid`, `geography`, `geometry` | `bytea` | SQL Server's serialized representation is preserved as opaque bytes, not converted to PostGIS or another native type. |
| `sql_variant` | `text` | Values are stringified, so their per-value source type metadata is not preserved. |

Computed columns are currently materialized as ordinary PostgreSQL columns: their values are copied, but their generation expressions are not recreated.

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
- `DATEADD()` for year, quarter, month, day, week, hour, minute, second, and millisecond units
- `DATEDIFF()` for year, quarter, month, day, week, hour, minute, second, and millisecond units
- `IIF()`
- `STUFF()`
- `REPLICATE()`
- `SPACE()`
- safe two-argument `CONVERT()` forms
- `CAST()` from common MSSQL-specific types
- single-argument `LOG()`

Rewrites avoid string literals and SQL comments. If a source definition falls outside the supported translation rules, ms2pg returns an explicit error describing the unsupported token or expression.

`DATALENGTH()` maps to PostgreSQL `OCTET_LENGTH()`, which measures the target encoding. Its result can differ for Unicode strings because SQL Server and PostgreSQL use different encodings.

## Not migrated

The following SQL Server-specific features are not currently recreated:

- stored procedures, user-defined functions, and triggers
- standalone sequences and synonyms
- users, roles, grants, and ownership
- partitioning, filegroups, compression, and memory-optimized table properties
- temporal-table behavior, change tracking, CDC, replication, and Service Broker
- full-text, XML, spatial, columnstore, hash, and JSON indexes
- exact SQL Server collation behavior
- computed-column expressions and rowversion generation

Views, defaults, checks, and filtered-index predicates are translated only when they match the documented normalization rules. The tool does not attempt to act as a general T-SQL compiler.

Disabled indexes and disabled check/foreign-key constraints are rejected because PostgreSQL cannot preserve their enforcement state directly. Enabled but untrusted check constraints and foreign keys are created as `NOT VALID`.

## Filtering

The migrator supports selective runs with:

- `-include-schemas`
- `-include-tables`
- `-exclude-schemas`
- `-exclude-tables`

Each filter accepts a comma-separated list of glob patterns. Table filters can be unqualified, such as `users`, or schema-qualified, such as `reporting.user_names` or `sales.*`. Exclude filters are applied after include filters.

Foreign keys that point to tables outside the selected migration set are skipped automatically so filtered runs still produce a valid target schema.

## Requirements

- Go 1.27.1+
- access to a SQL Server source database
- access to PostgreSQL 15+ (required to preserve SQL Server nullable-unique semantics)
- Docker, if you want to run the container-backed integration tests

## Testing

Fast tests:

```sh
go test -short ./...
```

Full tests, including Docker-backed SQL Server and PostgreSQL integration tests:

```sh
go test ./...
```

With Task:

```sh
task test
task test-integration
```

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
- `-version`: print the build version and exit
- `-include-schemas`: comma-separated schema filters with glob support
- `-include-tables`: comma-separated table and view filters with glob support
- `-exclude-schemas`: comma-separated schema filters to skip
- `-exclude-tables`: comma-separated table and view filters to skip

Both `-source` and `-target` are required.


## License

This project is licensed under the MIT License. See [LICENSE](LICENSE).
