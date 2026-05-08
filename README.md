# pgloader-go

`pgloader-go` is a first-cut Go implementation of a focused MSSQL to PostgreSQL migrator.

Current scope:

- introspect MSSQL schemas, tables, columns, primary keys, secondary indexes, foreign keys, and views
- map common MSSQL scalar types into PostgreSQL types
- create PostgreSQL schemas and tables automatically
- copy table data with streaming PostgreSQL `COPY`
- recreate source views after table data is loaded
- create supported secondary indexes, including PostgreSQL-safe filtered indexes and included columns, after data load
- support `-schema-only` for object creation without table row copy
- support schema and table/view filtering with `-include-schemas`, `-include-tables`, `-exclude-schemas`, and `-exclude-tables`

Current limitations:

- no triggers, procedures, or functions yet
- view SQL normalization is still selective; supported rewrites currently include bracketed identifiers, `SET ANSI_NULLS`, `SET QUOTED_IDENTIFIER`, `ISNULL`, `N'...'`, `GETDATE()`, `GETUTCDATE()`, `NEWID()`, `LEN()`, and `DATALENGTH()`
- schema-only mode creates views against empty target tables, so those views are created but return no rows until data is loaded
- filtered indexes are only recreated when the predicate is directly portable after normalization
- foreign key support is limited to straightforward referential constraints surfaced by `INFORMATION_SCHEMA`
- unsupported MSSQL types fail fast during catalog mapping

## Usage

```powershell
go run . \
  -source "sqlserver://sa:yourStrong(!)Password@localhost:1433?database=source_db&encrypt=disable" \
  -target "postgres://postgres:postgres@localhost:5432/target_db?sslmode=disable"
```

Filtered migration:

```powershell
go run . \
  -source "sqlserver://sa:yourStrong(!)Password@localhost:1433?database=source_db&encrypt=disable" \
  -target "postgres://postgres:postgres@localhost:5432/target_db?sslmode=disable" \
  -include-schemas "dbo,reporting" \
  -include-tables "users,reporting.user_names,reporting.user_labels,reporting.user_metrics" \
  -exclude-tables "reporting.legacy_*"
```

`-include-schemas`, `-include-tables`, `-exclude-schemas`, and `-exclude-tables` accept comma-separated glob patterns. Table filters can be unqualified (`users`) or schema-qualified (`reporting.user_names`, `sales.*`). Exclude filters are applied after include filters.

Schema-only migration:

```powershell
go run . \
  -source "sqlserver://sa:yourStrong(!)Password@localhost:1433?database=source_db&encrypt=disable" \
  -target "postgres://postgres:postgres@localhost:5432/target_db?sslmode=disable" \
  -schema-only
```

In schema-only mode, tables, primary keys, secondary indexes, foreign keys, and views are created, but table rows are not copied.

Verbose logging:

```powershell
go run . \
  -source "..." \
  -target "..." \
  -verbose
```

## Next implementation targets

- broaden integration coverage for more MSSQL edge cases and unsupported view forms
- add richer translation for more T-SQL built-ins and expression forms beyond the current normalization rules
- broaden compatibility checks for filtered index predicates that are only partially portable
- add migration support for more MSSQL objects beyond tables, views, indexes, and foreign keys

## Integration tests

The repository includes Docker-backed end-to-end tests using Testcontainers. They start MSSQL and PostgreSQL containers, seed a small source schema, and verify:

- full migration of tables, indexes, foreign keys, and a supported view
- PostgreSQL-safe filtered index and included-column migration
- composite index and composite foreign key migration
- clear failure for unsupported T-SQL view definitions
- schema-only migration creates objects without copying rows
- include-filtered and exclude-filtered migration only creates the selected schemas/tables/views

Run it with:

```powershell
go test ./...
```

If you want to skip container-backed tests in a quick local pass, use:

```powershell
go test -short ./...
```