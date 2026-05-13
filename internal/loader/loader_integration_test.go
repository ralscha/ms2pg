package loader_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"ms2pg/internal/loader"
)

const (
	mssqlPassword = "Ms2pg_StrongPassw0rd!"
	postgresUser  = "postgres"
	postgresPass  = "postgres"
	postgresDB    = "ms2pg_test"
)

func TestRunnerMigratesTablesAndViewsWithContainers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping container-backed integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	mssqlContainer := startMSSQLContainer(ctx, t)
	postgresContainer := startPostgresContainer(ctx, t)

	adminDSN := buildMSSQLDSN(t, ctx, mssqlContainer, "master")
	sourceDBName := "ms2pg_it"
	mustExecMSSQL(ctx, t, adminDSN, "CREATE DATABASE ["+sourceDBName+"]")

	sourceDSN := buildMSSQLDSN(t, ctx, mssqlContainer, sourceDBName)
	seedSourceSchema(ctx, t, sourceDSN)

	targetDSN := buildPostgresDSN(t, ctx, postgresContainer, postgresDB)

	runner := loader.Runner{
		Config: loader.Config{
			SourceDSN: sourceDSN,
			TargetDSN: targetDSN,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	assertTargetTableRows(ctx, t, targetDSN)
	assertTargetDefaultConstraintsMetadataOnly(ctx, t, targetDSN)
	assertTargetUniqueConstraintsMetadataOnly(ctx, t, targetDSN)
	assertTargetForeignKeys(ctx, t, targetDSN)
	assertTargetIndexes(ctx, t, targetDSN)
	assertTargetViewRows(ctx, t, targetDSN)
	assertCharIndexViewRows(ctx, t, targetDSN)
	assertDateAddViewRows(ctx, t, targetDSN)
	assertTargetCheckConstraintsMetadataOnly(ctx, t, targetDSN)
	assertDateDiffViewRows(ctx, t, targetDSN)
	assertTrimmedViewRows(ctx, t, targetDSN)
	assertGeneratedTokenViewRows(ctx, t, targetDSN)
}

func TestRunnerFailsClearlyForUnsupportedViewDefinitions(t *testing.T) {
	testCases := []struct {
		name           string
		sourceDBName   string
		viewDefinition string
		wantToken      string
	}{
		{
			name:         "top clause",
			sourceDBName: "ms2pg_badview_top_it",
			viewDefinition: `CREATE VIEW reporting.latest_user AS
		 SELECT TOP (1) u.id, u.name
		 FROM dbo.users u
		 ORDER BY u.id DESC`,
			wantToken: `TOP `,
		},
		{
			name:         "cross apply",
			sourceDBName: "ms2pg_badview_crossapply_it",
			viewDefinition: `CREATE VIEW reporting.user_name_lengths AS
		 SELECT u.id, metrics.name_len
		 FROM dbo.users u
		 CROSS APPLY (SELECT LEN(u.name) AS name_len) AS metrics`,
			wantToken: `CROSS APPLY`,
		},
		{
			name:         "try convert",
			sourceDBName: "ms2pg_badview_tryconvert_it",
			viewDefinition: `CREATE VIEW reporting.user_name_numbers AS
		 SELECT u.id, TRY_CONVERT(INT, u.name) AS parsed_name
		 FROM dbo.users u`,
			wantToken: `TRY_CONVERT(`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if testing.Short() {
				t.Skip("skipping container-backed integration test in short mode")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()

			mssqlContainer := startMSSQLContainer(ctx, t)
			postgresContainer := startPostgresContainer(ctx, t)

			adminDSN := buildMSSQLDSN(t, ctx, mssqlContainer, "master")
			mustExecMSSQL(ctx, t, adminDSN, "CREATE DATABASE ["+testCase.sourceDBName+"]")

			sourceDSN := buildMSSQLDSN(t, ctx, mssqlContainer, testCase.sourceDBName)
			seedUnsupportedViewSchema(ctx, t, sourceDSN, testCase.viewDefinition)

			targetDSN := buildPostgresDSN(t, ctx, postgresContainer, postgresDB)

			runner := loader.Runner{
				Config: loader.Config{
					SourceDSN: sourceDSN,
					TargetDSN: targetDSN,
				},
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			}

			err := runner.Run(ctx)
			if err == nil {
				t.Fatal("Run returned nil error, want unsupported view failure")
			}
			if !strings.Contains(err.Error(), "unsupported MSSQL view definition") {
				t.Fatalf("Run error = %v, want unsupported view definition message", err)
			}
			if !strings.Contains(err.Error(), testCase.wantToken) {
				t.Fatalf("Run error = %v, want unsupported token detail %q", err, testCase.wantToken)
			}
		})
	}
}

func TestRunnerFailsClearlyForUnsupportedCheckConstraintDefinitions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping container-backed integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	mssqlContainer := startMSSQLContainer(ctx, t)
	postgresContainer := startPostgresContainer(ctx, t)

	adminDSN := buildMSSQLDSN(t, ctx, mssqlContainer, "master")
	sourceDBName := "ms2pg_badcheck_it"
	mustExecMSSQL(ctx, t, adminDSN, "CREATE DATABASE ["+sourceDBName+"]")

	sourceDSN := buildMSSQLDSN(t, ctx, mssqlContainer, sourceDBName)
	seedUnsupportedCheckConstraintSchema(ctx, t, sourceDSN)

	targetDSN := buildPostgresDSN(t, ctx, postgresContainer, postgresDB)

	runner := loader.Runner{
		Config: loader.Config{
			SourceDSN: sourceDSN,
			TargetDSN: targetDSN,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := runner.Run(ctx)
	if err == nil {
		t.Fatal("Run returned nil error, want unsupported check constraint failure")
	}
	if !strings.Contains(err.Error(), "unsupported MSSQL check constraint definition") {
		t.Fatalf("Run error = %v, want unsupported check constraint definition message", err)
	}
	if !strings.Contains(err.Error(), `CONVERT(`) {
		t.Fatalf("Run error = %v, want unsupported token detail", err)
	}
}

func TestRunnerFailsClearlyForUnsupportedDefaultConstraintDefinitions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping container-backed integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	mssqlContainer := startMSSQLContainer(ctx, t)
	postgresContainer := startPostgresContainer(ctx, t)

	adminDSN := buildMSSQLDSN(t, ctx, mssqlContainer, "master")
	sourceDBName := "ms2pg_baddefault_it"
	mustExecMSSQL(ctx, t, adminDSN, "CREATE DATABASE ["+sourceDBName+"]")

	sourceDSN := buildMSSQLDSN(t, ctx, mssqlContainer, sourceDBName)
	seedUnsupportedDefaultConstraintSchema(ctx, t, sourceDSN)

	targetDSN := buildPostgresDSN(t, ctx, postgresContainer, postgresDB)

	runner := loader.Runner{
		Config: loader.Config{
			SourceDSN: sourceDSN,
			TargetDSN: targetDSN,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := runner.Run(ctx)
	if err == nil {
		t.Fatal("Run returned nil error, want unsupported default constraint failure")
	}
	if !strings.Contains(err.Error(), "unsupported MSSQL default constraint definition") {
		t.Fatalf("Run error = %v, want unsupported default constraint definition message", err)
	}
	if !strings.Contains(err.Error(), `CONVERT(`) {
		t.Fatalf("Run error = %v, want unsupported token detail", err)
	}
}

func TestRunnerSchemaOnlyCreatesObjectsWithoutCopyingRows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping container-backed integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	mssqlContainer := startMSSQLContainer(ctx, t)
	postgresContainer := startPostgresContainer(ctx, t)

	adminDSN := buildMSSQLDSN(t, ctx, mssqlContainer, "master")
	sourceDBName := "ms2pg_schemaonly_it"
	mustExecMSSQL(ctx, t, adminDSN, "CREATE DATABASE ["+sourceDBName+"]")

	sourceDSN := buildMSSQLDSN(t, ctx, mssqlContainer, sourceDBName)
	seedSourceSchema(ctx, t, sourceDSN)

	targetDSN := buildPostgresDSN(t, ctx, postgresContainer, postgresDB)

	runner := loader.Runner{
		Config: loader.Config{
			SourceDSN:  sourceDSN,
			TargetDSN:  targetDSN,
			SchemaOnly: true,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	assertSchemaOnlyObjects(ctx, t, targetDSN)
	assertSchemaOnlyRows(ctx, t, targetDSN)
	assertSchemaOnlyViewRows(ctx, t, targetDSN)
}

func TestRunnerAppliesSchemaAndTableFilters(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping container-backed integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	mssqlContainer := startMSSQLContainer(ctx, t)
	postgresContainer := startPostgresContainer(ctx, t)

	adminDSN := buildMSSQLDSN(t, ctx, mssqlContainer, "master")
	sourceDBName := "ms2pg_filters_it"
	mustExecMSSQL(ctx, t, adminDSN, "CREATE DATABASE ["+sourceDBName+"]")

	sourceDSN := buildMSSQLDSN(t, ctx, mssqlContainer, sourceDBName)
	seedSourceSchema(ctx, t, sourceDSN)

	targetDSN := buildPostgresDSN(t, ctx, postgresContainer, postgresDB)

	runner := loader.Runner{
		Config: loader.Config{
			SourceDSN:      sourceDSN,
			TargetDSN:      targetDSN,
			IncludeSchemas: []string{"dbo", "reporting"},
			IncludeTables:  []string{"users", "reporting.user_names", "reporting.user_labels", "reporting.user_metrics"},
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	assertTargetTableRows(ctx, t, targetDSN)
	assertFilteredIndexes(ctx, t, targetDSN)
	assertFilteredObjectsAbsent(ctx, t, targetDSN)
	assertTargetViewRows(ctx, t, targetDSN)
	assertNormalizedViewRows(ctx, t, targetDSN)
	assertExtendedNormalizedViewRows(ctx, t, targetDSN)
}

func TestRunnerAppliesExcludeFilters(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping container-backed integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	mssqlContainer := startMSSQLContainer(ctx, t)
	postgresContainer := startPostgresContainer(ctx, t)

	adminDSN := buildMSSQLDSN(t, ctx, mssqlContainer, "master")
	sourceDBName := "ms2pg_excludes_it"
	mustExecMSSQL(ctx, t, adminDSN, "CREATE DATABASE ["+sourceDBName+"]")

	sourceDSN := buildMSSQLDSN(t, ctx, mssqlContainer, sourceDBName)
	seedSourceSchema(ctx, t, sourceDSN)

	targetDSN := buildPostgresDSN(t, ctx, postgresContainer, postgresDB)

	runner := loader.Runner{
		Config: loader.Config{
			SourceDSN:      sourceDSN,
			TargetDSN:      targetDSN,
			ExcludeSchemas: []string{"sales"},
			ExcludeTables:  []string{"reporting.user_metrics"},
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	assertTargetTableRows(ctx, t, targetDSN)
	assertExcludeFilteredObjectsAbsent(ctx, t, targetDSN)
	assertTargetViewRows(ctx, t, targetDSN)
}

func TestRunnerSkipsNonPortableFilteredIndex(t *testing.T) {
	runSkippedNonPortableFilteredIndexTest(
		t,
		"ms2pg_badfilteredindex_convert_it",
		`CREATE INDEX idx_users_city_convert ON dbo.users (name) WHERE city = CONVERT(nvarchar(100), N'London')`,
		"idx_users_city_convert",
	)
}

func TestRunnerSkipsNonPortableDatetimeConvertFilteredIndex(t *testing.T) {
	runSkippedNonPortableFilteredIndexTest(
		t,
		"ms2pg_badfilteredindex_datetimeconvert_it",
		`CREATE INDEX idx_users_created_recent ON dbo.users (name) WHERE created_at >= CONVERT(datetime2, '2000-01-01T00:00:00')`,
		"idx_users_created_recent",
	)
}

func TestRunnerSkipsNonPortableCastFilteredIndex(t *testing.T) {
	runSkippedNonPortableFilteredIndexTest(
		t,
		"ms2pg_badfilteredindex_cast_it",
		`CREATE INDEX idx_users_city_cast ON dbo.users (name) WHERE city = CAST(N'London' AS nvarchar(100))`,
		"idx_users_city_cast",
	)
}

func runSkippedNonPortableFilteredIndexTest(t *testing.T, sourceDBName string, createIndexStatement string, indexName string) {
	if testing.Short() {
		t.Skip("skipping container-backed integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	mssqlContainer := startMSSQLContainer(ctx, t)
	postgresContainer := startPostgresContainer(ctx, t)

	adminDSN := buildMSSQLDSN(t, ctx, mssqlContainer, "master")
	mustExecMSSQL(ctx, t, adminDSN, "CREATE DATABASE ["+sourceDBName+"]")

	sourceDSN := buildMSSQLDSN(t, ctx, mssqlContainer, sourceDBName)
	seedSourceSchema(ctx, t, sourceDSN)
	mustExecMSSQL(ctx, t, sourceDSN, createIndexStatement)

	targetDSN := buildPostgresDSN(t, ctx, postgresContainer, postgresDB)

	runner := loader.Runner{
		Config: loader.Config{
			SourceDSN: sourceDSN,
			TargetDSN: targetDSN,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	assertTargetTableRows(ctx, t, targetDSN)
	assertTargetIndexes(ctx, t, targetDSN)
	assertTargetViewRows(ctx, t, targetDSN)
	assertSkippedIndexAbsent(ctx, t, targetDSN, "dbo", "users", indexName)
}

func startMSSQLContainer(ctx context.Context, t *testing.T) testcontainers.Container {
	t.Helper()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "mcr.microsoft.com/mssql/server:2022-latest",
			ExposedPorts: []string{"1433/tcp"},
			Env: map[string]string{
				"ACCEPT_EULA":       "Y",
				"MSSQL_SA_PASSWORD": mssqlPassword,
			},
			WaitingFor: wait.ForAll(
				wait.ForListeningPort("1433/tcp"),
				wait.ForLog("SQL Server is now ready for client connections"),
			).WithDeadline(3 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start MSSQL container: %v", err)
	}

	t.Cleanup(func() {
		terminateContainer(context.Background(), t, container)
	})

	return container
}

func startPostgresContainer(ctx context.Context, t *testing.T) testcontainers.Container {
	t.Helper()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER":     postgresUser,
				"POSTGRES_PASSWORD": postgresPass,
				"POSTGRES_DB":       postgresDB,
			},
			WaitingFor: wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections"),
			).WithDeadline(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start PostgreSQL container: %v", err)
	}

	t.Cleanup(func() {
		terminateContainer(context.Background(), t, container)
	})

	dsn := buildPostgresDSN(t, ctx, container, postgresDB)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL connection: %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	if err := waitForPing(ctx, db.PingContext); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	return container
}

func seedSourceSchema(ctx context.Context, t *testing.T, sourceDSN string) {
	t.Helper()

	statements := []string{
		`CREATE SCHEMA reporting`,
		`CREATE SCHEMA sales`,
		`CREATE TABLE dbo.users (
			id INT IDENTITY(1,1) PRIMARY KEY,
			name NVARCHAR(100) NOT NULL,
			city NVARCHAR(100) NULL,
			created_at DATETIME2 NOT NULL CONSTRAINT df_users_created_at DEFAULT GETDATE(),
			external_id UNIQUEIDENTIFIER NOT NULL CONSTRAINT df_users_external_id DEFAULT NEWID(),
			CONSTRAINT uq_users_external_id UNIQUE (external_id),
			CONSTRAINT ck_users_name_nonempty CHECK (LEN(name) > 0 AND name <> N''),
			CONSTRAINT ck_users_city_trimmed CHECK (LTRIM(RTRIM(city)) <> N'')
		)`,
		`CREATE TABLE sales.orders (
			id INT IDENTITY(1,1) PRIMARY KEY,
			user_id INT NOT NULL,
			order_ref NVARCHAR(50) NOT NULL,
			CONSTRAINT fk_orders_user_id FOREIGN KEY (user_id) REFERENCES dbo.users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE sales.user_regions (
			user_id INT NOT NULL,
			region_code NVARCHAR(10) NOT NULL,
			label NVARCHAR(50) NOT NULL,
			CONSTRAINT pk_user_regions PRIMARY KEY (user_id, region_code),
			CONSTRAINT fk_user_regions_user_id FOREIGN KEY (user_id) REFERENCES dbo.users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE sales.order_regions (
			id INT IDENTITY(1,1) PRIMARY KEY,
			user_id INT NOT NULL,
			region_code NVARCHAR(10) NOT NULL,
			note NVARCHAR(50) NOT NULL,
			CONSTRAINT fk_order_regions_user_region FOREIGN KEY (user_id, region_code)
				REFERENCES sales.user_regions(user_id, region_code) ON UPDATE CASCADE
		)`,
		`INSERT INTO dbo.users (name, city) VALUES ('Ada Lovelace', 'London'), ('Grace Hopper', 'New York')`,
		`INSERT INTO sales.orders (user_id, order_ref) VALUES (1, 'A-100'), (2, 'G-200')`,
		`INSERT INTO sales.user_regions (user_id, region_code, label) VALUES (1, 'EU', 'Europe'), (2, 'US', 'United States')`,
		`INSERT INTO sales.order_regions (user_id, region_code, note) VALUES (1, 'EU', 'priority'), (2, 'US', 'standard')`,
		`CREATE UNIQUE INDEX idx_users_name_city ON dbo.users (name, city)`,
		`CREATE INDEX idx_users_name_filtered ON dbo.users (name) INCLUDE (city) WHERE city IS NOT NULL`,
		`CREATE INDEX idx_orders_user_ref ON sales.orders (user_id, order_ref)`,
		`CREATE VIEW reporting.user_names AS
		 SELECT u.id, u.name, u.city
		 FROM dbo.users u
		 WHERE u.id > 0`,
		`CREATE VIEW [reporting].[user_labels] AS
		 SELECT [u].[id], ISNULL([u].[city], N'unknown') AS [city_label], GETDATE() AS [generated_at]
		 FROM [dbo].[users] AS [u]`,
		`CREATE VIEW [reporting].[user_metrics] AS
		 SELECT [u].[id], LEN([u].[name]) AS [name_len], DATALENGTH([u].[name]) AS [name_bytes], GETUTCDATE() AS [generated_utc]
		 FROM [dbo].[users] AS [u]`,
		`CREATE VIEW [reporting].[user_positions] AS
		 SELECT [u].[id], CHARINDEX(N'a', [u].[name]) AS [first_a]
		 FROM [dbo].[users] AS [u]`,
		`CREATE VIEW [reporting].[user_schedule] AS
		 SELECT [u].[id], DATEADD(day, 1, [u].[created_at]) AS [created_plus_day]
		 FROM [dbo].[users] AS [u]`,
		`CREATE VIEW [reporting].[user_day_span] AS
		 SELECT [u].[id], DATEDIFF(day, '2000-01-01', '2000-01-03') AS [day_span]
		 FROM [dbo].[users] AS [u]
		 WHERE [u].[id] = 1`,
		`CREATE VIEW [reporting].[user_trimmed] AS
		 SELECT [u].[id], LTRIM(RTRIM(N'  padded value  ')) AS [trimmed_label]
		 FROM [dbo].[users] AS [u]
		 WHERE [u].[id] = 1`,
		`CREATE VIEW [reporting].[user_tokens] AS
		 SELECT [u].[id], NEWID() AS [generated_token], N'hello' AS [greeting]
		 FROM [dbo].[users] AS [u]
		 WHERE [u].[id] = 1`,
	}

	for _, statement := range statements {
		mustExecMSSQL(ctx, t, sourceDSN, statement)
	}
}

func assertTargetForeignKeys(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for foreign key assertions: %v", err)
	}
	defer pool.Close()

	var constraintName string
	var updateRule string
	var deleteRule string
	if err := pool.QueryRow(ctx, `
		SELECT tc.constraint_name, rc.update_rule, rc.delete_rule
		FROM information_schema.table_constraints tc
		JOIN information_schema.referential_constraints rc
		  ON rc.constraint_name = tc.constraint_name
		 AND rc.constraint_schema = tc.constraint_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema = 'sales'
		  AND tc.table_name = 'orders'`).Scan(&constraintName, &updateRule, &deleteRule); err != nil {
		t.Fatalf("query migrated foreign key: %v", err)
	}
	if constraintName != "fk_orders_user_id" {
		t.Fatalf("foreign key name = %q, want fk_orders_user_id", constraintName)
	}
	if updateRule != "NO ACTION" {
		t.Fatalf("foreign key update rule = %q, want NO ACTION", updateRule)
	}
	if deleteRule != "CASCADE" {
		t.Fatalf("foreign key delete rule = %q, want CASCADE", deleteRule)
	}

	assertCompositeForeignKey(ctx, t, pool)

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM "sales"."orders"`).Scan(&count); err != nil {
		t.Fatalf("count migrated orders: %v", err)
	}
	if count != 2 {
		t.Fatalf("migrated orders count = %d, want 2", count)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM "sales"."order_regions"`).Scan(&count); err != nil {
		t.Fatalf("count migrated order_regions: %v", err)
	}
	if count != 2 {
		t.Fatalf("migrated order_regions count = %d, want 2", count)
	}
}

func assertCompositeForeignKey(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.constraint_schema = kcu.constraint_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema = 'sales'
		  AND tc.table_name = 'order_regions'
		  AND tc.constraint_name = 'fk_order_regions_user_region'
		ORDER BY kcu.ordinal_position`)
	if err != nil {
		t.Fatalf("query composite foreign key local column order: %v", err)
	}
	defer rows.Close()

	var gotLocal []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan composite foreign key local column order: %v", err)
		}
		gotLocal = append(gotLocal, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate composite foreign key local column order: %v", err)
	}
	if strings.Join(gotLocal, ",") != "user_id,region_code" {
		t.Fatalf("composite foreign key local columns = %v, want [user_id region_code]", gotLocal)
	}

	var updateRule string
	var deleteRule string
	if err := pool.QueryRow(ctx, `
		SELECT rc.update_rule, rc.delete_rule
		FROM information_schema.referential_constraints rc
		WHERE rc.constraint_schema = 'sales'
		  AND rc.constraint_name = 'fk_order_regions_user_region'`).Scan(&updateRule, &deleteRule); err != nil {
		t.Fatalf("query composite foreign key actions: %v", err)
	}
	if updateRule != "CASCADE" {
		t.Fatalf("composite foreign key update rule = %q, want CASCADE", updateRule)
	}
	if deleteRule != "NO ACTION" {
		t.Fatalf("composite foreign key delete rule = %q, want NO ACTION", deleteRule)
	}
}

func assertSchemaOnlyObjects(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for schema-only assertions: %v", err)
	}
	defer pool.Close()

	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'dbo' AND table_name = 'users'
		)`).Scan(&exists); err != nil {
		t.Fatalf("query schema-only users table existence: %v", err)
	}
	if !exists {
		t.Fatal("dbo.users was not created in schema-only mode")
	}

	assertTargetIndexes(ctx, t, targetDSN)
	assertTargetDefaultConstraintsMetadataOnly(ctx, t, targetDSN)
	assertTargetUniqueConstraintsMetadataOnly(ctx, t, targetDSN)
	assertTargetCheckConstraintsMetadataOnly(ctx, t, targetDSN)
	assertTargetForeignKeysMetadataOnly(ctx, t, targetDSN)

	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.views
			WHERE table_schema = 'reporting' AND table_name = 'user_names'
		)`).Scan(&exists); err != nil {
		t.Fatalf("query schema-only view existence: %v", err)
	}
	if !exists {
		t.Fatal("reporting.user_names was not created in schema-only mode")
	}
}

func assertSchemaOnlyRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for schema-only row assertions: %v", err)
	}
	defer pool.Close()

	for _, query := range []struct {
		name string
		sql  string
	}{
		{name: "dbo.users", sql: `SELECT COUNT(*) FROM "dbo"."users"`},
		{name: "sales.orders", sql: `SELECT COUNT(*) FROM "sales"."orders"`},
	} {
		var count int
		if err := pool.QueryRow(ctx, query.sql).Scan(&count); err != nil {
			t.Fatalf("count schema-only rows for %s: %v", query.name, err)
		}
		if count != 0 {
			t.Fatalf("schema-only row count for %s = %d, want 0", query.name, count)
		}
	}
}

func assertSchemaOnlyViewRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for schema-only view assertions: %v", err)
	}
	defer pool.Close()

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM "reporting"."user_names"`).Scan(&count); err != nil {
		t.Fatalf("count schema-only view rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("schema-only view row count = %d, want 0", count)
	}
}

func assertTargetForeignKeysMetadataOnly(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for foreign key metadata assertions: %v", err)
	}
	defer pool.Close()

	var constraintName string
	var updateRule string
	var deleteRule string
	if err := pool.QueryRow(ctx, `
		SELECT tc.constraint_name, rc.update_rule, rc.delete_rule
		FROM information_schema.table_constraints tc
		JOIN information_schema.referential_constraints rc
		  ON rc.constraint_name = tc.constraint_name
		 AND rc.constraint_schema = tc.constraint_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema = 'sales'
		  AND tc.table_name = 'orders'`).Scan(&constraintName, &updateRule, &deleteRule); err != nil {
		t.Fatalf("query migrated foreign key metadata: %v", err)
	}
	if constraintName != "fk_orders_user_id" {
		t.Fatalf("foreign key name = %q, want fk_orders_user_id", constraintName)
	}
	if updateRule != "NO ACTION" {
		t.Fatalf("foreign key update rule = %q, want NO ACTION", updateRule)
	}
	if deleteRule != "CASCADE" {
		t.Fatalf("foreign key delete rule = %q, want CASCADE", deleteRule)
	}
}

func assertTargetDefaultConstraintsMetadataOnly(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for default metadata assertions: %v", err)
	}
	defer pool.Close()

	checks := []struct {
		column string
		want   string
	}{
		{column: "created_at", want: "current_timestamp"},
		{column: "external_id", want: "gen_random_uuid()"},
	}

	for _, check := range checks {
		var columnDefault string
		if err := pool.QueryRow(ctx, `
			SELECT column_default
			FROM information_schema.columns
			WHERE table_schema = 'dbo'
			  AND table_name = 'users'
			  AND column_name = $1`, check.column).Scan(&columnDefault); err != nil {
			t.Fatalf("query migrated default metadata for %s: %v", check.column, err)
		}
		if !strings.Contains(strings.ToLower(columnDefault), check.want) {
			t.Fatalf("default for %s = %q, want substring %q", check.column, columnDefault, check.want)
		}
	}
}

func assertTargetUniqueConstraintsMetadataOnly(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for unique constraint metadata assertions: %v", err)
	}
	defer pool.Close()

	var constraintName string
	if err := pool.QueryRow(ctx, `
		SELECT tc.constraint_name
		FROM information_schema.table_constraints tc
		WHERE tc.constraint_type = 'UNIQUE'
		  AND tc.table_schema = 'dbo'
		  AND tc.table_name = 'users'
		  AND tc.constraint_name = 'uq_users_external_id'`).Scan(&constraintName); err != nil {
		t.Fatalf("query migrated unique constraint metadata: %v", err)
	}
	if constraintName != "uq_users_external_id" {
		t.Fatalf("unique constraint name = %q, want uq_users_external_id", constraintName)
	}

	rows, err := pool.Query(ctx, `
		SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON kcu.constraint_schema = tc.constraint_schema
		 AND kcu.constraint_name = tc.constraint_name
		WHERE tc.constraint_type = 'UNIQUE'
		  AND tc.table_schema = 'dbo'
		  AND tc.table_name = 'users'
		  AND tc.constraint_name = 'uq_users_external_id'
		ORDER BY kcu.ordinal_position`)
	if err != nil {
		t.Fatalf("query migrated unique constraint columns: %v", err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan migrated unique constraint column: %v", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated unique constraint columns: %v", err)
	}
	if strings.Join(columns, ",") != "external_id" {
		t.Fatalf("unique constraint columns = %v, want [external_id]", columns)
	}
}

func assertTargetCheckConstraintsMetadataOnly(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for check constraint metadata assertions: %v", err)
	}
	defer pool.Close()

	var constraintName string
	var checkClause string
	if err := pool.QueryRow(ctx, `
		SELECT tc.constraint_name, cc.check_clause
		FROM information_schema.table_constraints tc
		JOIN information_schema.check_constraints cc
		  ON cc.constraint_schema = tc.constraint_schema
		 AND cc.constraint_name = tc.constraint_name
		WHERE tc.constraint_type = 'CHECK'
		  AND tc.table_schema = 'dbo'
		  AND tc.table_name = 'users'
		  AND tc.constraint_name = 'ck_users_name_nonempty'`).Scan(&constraintName, &checkClause); err != nil {
		t.Fatalf("query migrated check constraint metadata: %v", err)
	}
	if constraintName != "ck_users_name_nonempty" {
		t.Fatalf("check constraint name = %q, want ck_users_name_nonempty", constraintName)
	}
	if !strings.Contains(strings.ToLower(checkClause), "length") {
		t.Fatalf("check constraint clause = %q, want translated LENGTH expression", checkClause)
	}

	if err := pool.QueryRow(ctx, `
		SELECT cc.check_clause
		FROM information_schema.table_constraints tc
		JOIN information_schema.check_constraints cc
		  ON cc.constraint_schema = tc.constraint_schema
		 AND cc.constraint_name = tc.constraint_name
		WHERE tc.constraint_type = 'CHECK'
		  AND tc.table_schema = 'dbo'
		  AND tc.table_name = 'users'
		  AND tc.constraint_name = 'ck_users_city_trimmed'`).Scan(&checkClause); err != nil {
		t.Fatalf("query migrated trimmed check constraint metadata: %v", err)
	}
	lowerClause := strings.ToLower(checkClause)
	if !strings.Contains(lowerClause, "ltrim") || !strings.Contains(lowerClause, "rtrim") {
		t.Fatalf("trimmed check constraint clause = %q, want LTRIM/RTRIM expression", checkClause)
	}
}

func assertTargetIndexes(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for index assertions: %v", err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `
		SELECT a.attname
		FROM pg_indexes i
		JOIN pg_class idx ON idx.relname = i.indexname
		JOIN pg_index pi ON pi.indexrelid = idx.oid
		JOIN pg_class tbl ON tbl.oid = pi.indrelid
		JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		JOIN unnest(pi.indkey) WITH ORDINALITY AS cols(attnum, ord) ON TRUE
		JOIN pg_attribute a ON a.attrelid = tbl.oid AND a.attnum = cols.attnum
		WHERE ns.nspname = 'dbo' AND tbl.relname = 'users' AND i.indexname = 'idx_users_name_city'
		ORDER BY cols.ord`)
	if err != nil {
		t.Fatalf("query migrated index: %v", err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan migrated index column: %v", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated index columns: %v", err)
	}

	if strings.Join(columns, ",") != "name,city" {
		t.Fatalf("migrated index columns = %v, want [name city]", columns)
	}

	var unique bool
	if err := pool.QueryRow(ctx, `
		SELECT pi.indisunique
		FROM pg_indexes i
		JOIN pg_class idx ON idx.relname = i.indexname
		JOIN pg_index pi ON pi.indexrelid = idx.oid
		JOIN pg_class tbl ON tbl.oid = pi.indrelid
		JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		WHERE ns.nspname = 'dbo' AND tbl.relname = 'users' AND i.indexname = 'idx_users_name_city'`).Scan(&unique); err != nil {
		t.Fatalf("query migrated index uniqueness: %v", err)
	}
	if !unique {
		t.Fatal("migrated index is not unique, want unique")
	}

	assertCompositeIndex(ctx, t, pool)
	assertFilteredIncludedIndex(ctx, t, pool)
}

func assertCompositeIndex(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT a.attname
		FROM pg_indexes i
		JOIN pg_class idx ON idx.relname = i.indexname
		JOIN pg_index pi ON pi.indexrelid = idx.oid
		JOIN pg_class tbl ON tbl.oid = pi.indrelid
		JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		JOIN unnest(pi.indkey) WITH ORDINALITY AS cols(attnum, ord) ON TRUE
		JOIN pg_attribute a ON a.attrelid = tbl.oid AND a.attnum = cols.attnum
		WHERE ns.nspname = 'sales' AND tbl.relname = 'orders' AND i.indexname = 'idx_orders_user_ref'
		ORDER BY cols.ord`)
	if err != nil {
		t.Fatalf("query composite index: %v", err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan composite index column: %v", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate composite index columns: %v", err)
	}
	if strings.Join(columns, ",") != "user_id,order_ref" {
		t.Fatalf("composite index columns = %v, want [user_id order_ref]", columns)
	}
}

func assertFilteredIndexes(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for filtered index assertions: %v", err)
	}
	defer pool.Close()

	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'dbo' AND tablename = 'users' AND indexname = 'idx_users_name_city'
		)`).Scan(&exists); err != nil {
		t.Fatalf("query filtered users index: %v", err)
	}
	if !exists {
		t.Fatal("filtered migration did not create dbo.users composite index")
	}

	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'dbo' AND tablename = 'users' AND indexname = 'idx_users_name_filtered'
		)`).Scan(&exists); err != nil {
		t.Fatalf("query filtered users predicate index: %v", err)
	}
	if !exists {
		t.Fatal("filtered migration did not create dbo.users filtered index")
	}

	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'sales' AND tablename = 'orders' AND indexname = 'idx_orders_user_ref'
		)`).Scan(&exists); err != nil {
		t.Fatalf("query filtered sales index absence: %v", err)
	}
	if exists {
		t.Fatal("sales.orders index unexpectedly exists under filtered migration")
	}
}

func assertSkippedIndexAbsent(ctx context.Context, t *testing.T, targetDSN string, schema string, table string, indexName string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for skipped index assertions: %v", err)
	}
	defer pool.Close()

	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = $1 AND tablename = $2 AND indexname = $3
		)`, schema, table, indexName).Scan(&exists); err != nil {
		t.Fatalf("query skipped index %s.%s.%s: %v", schema, table, indexName, err)
	}
	if exists {
		t.Fatalf("index %s.%s.%s unexpectedly exists after migration", schema, table, indexName)
	}
}

func assertFilteredObjectsAbsent(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for filtered object assertions: %v", err)
	}
	defer pool.Close()

	checks := []struct {
		name  string
		query string
	}{
		{name: "sales.orders", query: `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'sales' AND table_name = 'orders')`},
		{name: "sales.user_regions", query: `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'sales' AND table_name = 'user_regions')`},
		{name: "sales.order_regions", query: `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'sales' AND table_name = 'order_regions')`},
	}

	for _, check := range checks {
		var exists bool
		if err := pool.QueryRow(ctx, check.query).Scan(&exists); err != nil {
			t.Fatalf("query filtered object %s: %v", check.name, err)
		}
		if exists {
			t.Fatalf("filtered object %s unexpectedly exists", check.name)
		}
	}
}

func assertExcludeFilteredObjectsAbsent(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for exclude filter assertions: %v", err)
	}
	defer pool.Close()

	checks := []struct {
		name  string
		query string
	}{
		{name: "sales.orders", query: `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'sales' AND table_name = 'orders')`},
		{name: "sales.user_regions", query: `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'sales' AND table_name = 'user_regions')`},
		{name: "sales.order_regions", query: `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'sales' AND table_name = 'order_regions')`},
		{name: "reporting.user_metrics", query: `SELECT EXISTS (SELECT 1 FROM information_schema.views WHERE table_schema = 'reporting' AND table_name = 'user_metrics')`},
	}

	for _, check := range checks {
		var exists bool
		if err := pool.QueryRow(ctx, check.query).Scan(&exists); err != nil {
			t.Fatalf("query excluded object %s: %v", check.name, err)
		}
		if exists {
			t.Fatalf("excluded object %s unexpectedly exists", check.name)
		}
	}
}

func assertNormalizedViewRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for normalized view assertions: %v", err)
	}
	defer pool.Close()

	var cityLabel string
	if err := pool.QueryRow(ctx, `SELECT city_label FROM "reporting"."user_labels" WHERE id = 1`).Scan(&cityLabel); err != nil {
		t.Fatalf("query normalized view row: %v", err)
	}
	if cityLabel != "London" {
		t.Fatalf("normalized view city_label = %q, want London", cityLabel)
	}
}

func assertExtendedNormalizedViewRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for extended normalized view assertions: %v", err)
	}
	defer pool.Close()

	var nameLen int
	var nameBytes int
	if err := pool.QueryRow(ctx, `SELECT name_len, name_bytes FROM "reporting"."user_metrics" WHERE id = 1`).Scan(&nameLen, &nameBytes); err != nil {
		t.Fatalf("query extended normalized view row: %v", err)
	}
	if nameLen != 12 {
		t.Fatalf("normalized view name_len = %d, want 12", nameLen)
	}
	if nameBytes != 12 {
		t.Fatalf("normalized view name_bytes = %d, want 12", nameBytes)
	}
}

func assertGeneratedTokenViewRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for generated token view assertions: %v", err)
	}
	defer pool.Close()

	var token string
	var greeting string
	if err := pool.QueryRow(ctx, `SELECT generated_token::text, greeting FROM "reporting"."user_tokens" WHERE id = 1`).Scan(&token, &greeting); err != nil {
		t.Fatalf("query generated token view row: %v", err)
	}
	if greeting != "hello" {
		t.Fatalf("generated token view greeting = %q, want hello", greeting)
	}
	if len(token) != 36 {
		t.Fatalf("generated token view token = %q, want 36-character UUID text", token)
	}
}

func assertCharIndexViewRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for CHARINDEX view assertions: %v", err)
	}
	defer pool.Close()

	var firstA int
	if err := pool.QueryRow(ctx, `SELECT first_a FROM "reporting"."user_positions" WHERE id = 1`).Scan(&firstA); err != nil {
		t.Fatalf("query CHARINDEX view row: %v", err)
	}
	if firstA != 3 {
		t.Fatalf("CHARINDEX view first_a = %d, want 3", firstA)
	}
}

func assertDateAddViewRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for DATEADD view assertions: %v", err)
	}
	defer pool.Close()

	var matches bool
	if err := pool.QueryRow(ctx, `
		SELECT (s.created_plus_day - u.created_at) = INTERVAL '1 day'
		FROM "reporting"."user_schedule" s
		JOIN "dbo"."users" u ON u.id = s.id
		WHERE s.id = 1`).Scan(&matches); err != nil {
		t.Fatalf("query DATEADD view row: %v", err)
	}
	if !matches {
		t.Fatal("DATEADD view did not preserve a one-day offset")
	}
}

func assertDateDiffViewRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for DATEDIFF view assertions: %v", err)
	}
	defer pool.Close()

	var daySpan int
	if err := pool.QueryRow(ctx, `SELECT day_span FROM "reporting"."user_day_span" WHERE id = 1`).Scan(&daySpan); err != nil {
		t.Fatalf("query DATEDIFF view row: %v", err)
	}
	if daySpan != 2 {
		t.Fatalf("DATEDIFF view day_span = %d, want 2", daySpan)
	}
}

func assertTrimmedViewRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for trimmed view assertions: %v", err)
	}
	defer pool.Close()

	var trimmedLabel string
	if err := pool.QueryRow(ctx, `SELECT trimmed_label FROM "reporting"."user_trimmed" WHERE id = 1`).Scan(&trimmedLabel); err != nil {
		t.Fatalf("query trimmed view row: %v", err)
	}
	if trimmedLabel != "padded value" {
		t.Fatalf("trimmed view label = %q, want padded value", trimmedLabel)
	}
}

func assertFilteredIncludedIndex(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	var indexDefinition string
	if err := pool.QueryRow(ctx, `
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = 'dbo' AND tablename = 'users' AND indexname = 'idx_users_name_filtered'`).Scan(&indexDefinition); err != nil {
		t.Fatalf("query filtered included index definition: %v", err)
	}

	wants := []string{
		`(name)`,
		`INCLUDE (city)`,
		`WHERE (city IS NOT NULL)`,
	}
	for _, want := range wants {
		if !strings.Contains(indexDefinition, want) {
			t.Fatalf("index definition %q does not contain %q", indexDefinition, want)
		}
	}
}

func seedUnsupportedViewSchema(ctx context.Context, t *testing.T, sourceDSN string, viewDefinition string) {
	t.Helper()

	statements := []string{
		`CREATE SCHEMA reporting`,
		`CREATE TABLE dbo.users (
			id INT IDENTITY(1,1) PRIMARY KEY,
			name NVARCHAR(100) NOT NULL
		)`,
		`INSERT INTO dbo.users (name) VALUES ('Ada Lovelace'), ('Grace Hopper')`,
		viewDefinition,
	}

	for _, statement := range statements {
		mustExecMSSQL(ctx, t, sourceDSN, statement)
	}
}

func seedUnsupportedCheckConstraintSchema(ctx context.Context, t *testing.T, sourceDSN string) {
	t.Helper()

	statements := []string{
		`CREATE TABLE dbo.users (
			id INT IDENTITY(1,1) PRIMARY KEY,
			name NVARCHAR(100) NOT NULL,
			city NVARCHAR(100) NOT NULL,
			CONSTRAINT ck_users_city_convert CHECK (city = CONVERT(nvarchar(100), N'London'))
		)`,
		`INSERT INTO dbo.users (name, city) VALUES ('Ada Lovelace', 'London')`,
	}

	for _, statement := range statements {
		mustExecMSSQL(ctx, t, sourceDSN, statement)
	}
}

func seedUnsupportedDefaultConstraintSchema(ctx context.Context, t *testing.T, sourceDSN string) {
	t.Helper()

	statements := []string{
		`CREATE TABLE dbo.users (
			id INT IDENTITY(1,1) PRIMARY KEY,
			name NVARCHAR(100) NOT NULL,
			city NVARCHAR(100) NOT NULL CONSTRAINT df_users_city_convert DEFAULT CONVERT(nvarchar(100), 'London')
		)`,
		`INSERT INTO dbo.users (name) VALUES ('Ada Lovelace')`,
	}

	for _, statement := range statements {
		mustExecMSSQL(ctx, t, sourceDSN, statement)
	}
}

func assertTargetTableRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for table assertions: %v", err)
	}
	defer pool.Close()

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM "dbo"."users"`).Scan(&count); err != nil {
		t.Fatalf("count migrated rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("migrated row count = %d, want 2", count)
	}

	var name string
	var city *string
	if err := pool.QueryRow(ctx, `SELECT name, city FROM "dbo"."users" WHERE id = 1`).Scan(&name, &city); err != nil {
		t.Fatalf("query migrated user: %v", err)
	}
	if name != "Ada Lovelace" {
		t.Fatalf("migrated user name = %q, want Ada Lovelace", name)
	}
	if city == nil || *city != "London" {
		t.Fatalf("migrated user city = %v, want London", city)
	}
}

func assertTargetViewRows(ctx context.Context, t *testing.T, targetDSN string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect target for view assertions: %v", err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `SELECT name, city FROM "reporting"."user_names" ORDER BY id`)
	if err != nil {
		t.Fatalf("query migrated view: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		var city *string
		if err := rows.Scan(&name, &city); err != nil {
			t.Fatalf("scan migrated view row: %v", err)
		}
		if city == nil {
			got = append(got, name+":<nil>")
			continue
		}
		got = append(got, name+":"+*city)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated view rows: %v", err)
	}

	want := []string{"Ada Lovelace:London", "Grace Hopper:New York"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("view rows = %v, want %v", got, want)
	}
}

func mustExecMSSQL(ctx context.Context, t *testing.T, dsn string, statement string) {
	t.Helper()

	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatalf("open MSSQL connection: %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	if err := waitForPing(ctx, db.PingContext); err != nil {
		t.Fatalf("ping MSSQL: %v", err)
	}

	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Fatalf("exec MSSQL statement %q: %v", statement, err)
	}
}

func buildMSSQLDSN(t *testing.T, ctx context.Context, container testcontainers.Container, database string) string {
	t.Helper()

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get MSSQL host: %v", err)
	}
	port, err := container.MappedPort(ctx, "1433/tcp")
	if err != nil {
		t.Fatalf("get MSSQL port: %v", err)
	}

	query := url.Values{}
	query.Set("database", database)
	query.Set("encrypt", "disable")

	return (&url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword("sa", mssqlPassword),
		Host:     fmt.Sprintf("%s:%s", host, port.Port()),
		RawQuery: query.Encode(),
	}).String()
}

func buildPostgresDSN(t *testing.T, ctx context.Context, container testcontainers.Container, database string) string {
	t.Helper()

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get PostgreSQL host: %v", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("get PostgreSQL port: %v", err)
	}

	query := url.Values{}
	query.Set("sslmode", "disable")

	return (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(postgresUser, postgresPass),
		Host:     fmt.Sprintf("%s:%s", host, port.Port()),
		Path:     database,
		RawQuery: query.Encode(),
	}).String()
}

func waitForPing(ctx context.Context, ping func(context.Context) error) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		if err := ping(ctx); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func terminateContainer(ctx context.Context, t *testing.T, container testcontainers.Container) {
	t.Helper()
	if err := container.Terminate(ctx); err != nil {
		t.Fatalf("terminate container: %v", err)
	}
}
