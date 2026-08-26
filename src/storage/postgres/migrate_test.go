package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OpenAgriNet/discovery-service/migrations"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// migrationsDir is where the .sql files live for `make migrate` and for
// tests/dbtest, both of which reach them through file://. This test is what
// keeps the third reader — the binary's own embedded copy — in step with them.
const migrationsDir = "../../../migrations"

// D10 says the migrations are `//go:embed`-able so the binary self-migrates,
// and an embed pattern is exactly the kind of thing that goes stale in silence:
// a migration added to the directory still runs under `make migrate` and under
// dbtest, both of which read the directory, and is simply absent from the image
// that was meant to apply it at boot. The failure surfaces as a missing column
// on a deployment nobody migrated by hand.
func TestEveryMigrationOnDiskIsAlsoEmbedded(t *testing.T) {
	onDisk := sqlFilesOnDisk(t)
	if len(onDisk) == 0 {
		t.Fatalf("no .sql files under %s; the walk is broken, not the embed", migrationsDir)
	}

	embedded, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("read the embedded migrations: %v", err)
	}

	var names []string
	for _, entry := range embedded {
		names = append(names, entry.Name())
	}
	for _, name := range onDisk {
		if !slices.Contains(names, name) {
			t.Errorf("%s is in %s and not in migrations.FS: the binary would boot a schema behind the repository",
				name, migrationsDir)
		}
	}
	if len(names) != len(onDisk) {
		t.Errorf("migrations.FS holds %d files and the directory holds %d", len(names), len(onDisk))
	}
}

// Every migration is a pair. golang-migrate applies the .up half and needs the
// .down half to roll back, and an embed that carried one without the other
// would leave a deployment able to migrate forward and not back.
func TestEveryEmbeddedMigrationIsAPair(t *testing.T) {
	for _, name := range sqlFilesOnDisk(t) {
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		down := strings.TrimSuffix(name, ".up.sql") + ".down.sql"
		if _, err := migrations.FS.ReadFile(down); err != nil {
			t.Errorf("%s is embedded and %s is not: %v", name, down, err)
		}
	}
}

func sqlFilesOnDisk(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read %s: %v", migrationsDir, err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".sql" {
			names = append(names, entry.Name())
		}
	}
	return names
}

// DATABASE_AUTO_MIGRATE is what makes the binary self-migrating, and the whole
// of that claim is that the copy inside the image reaches a real server. Every
// other migration test in this repository runs golang-migrate over the
// DIRECTORY through file://, which is the one path a deployed image does not
// have.
func TestMigrateBringsAFreshDatabaseUpFromTheEmbeddedCopy(t *testing.T) {
	dsn := dbtest.NewMigrationTarget(t)

	if err := postgres.Migrate(dsn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect to the migrated database: %v", err)
	}
	t.Cleanup(pool.Close)

	// resources is the table every discover query scans, so its absence is the
	// absence of the service.
	var exists bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                 WHERE table_schema = 'public' AND table_name = $1)`,
		"resources").Scan(&exists); err != nil {
		t.Fatalf("ask for the resources table: %v", err)
	}
	if !exists {
		t.Error("the resources table is absent after Migrate")
	}
}

// AutoMigrate runs on EVERY boot, not on the first one. A second call has
// nothing to apply, and golang-migrate reports that as ErrNoChange — an error
// value, which an unwary runner would turn into a crash loop on every restart
// of an already-migrated deployment.
func TestMigrateIsANoOpTheSecondTime(t *testing.T) {
	dsn := dbtest.NewMigrationTarget(t)

	if err := postgres.Migrate(dsn); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := postgres.Migrate(dsn); err != nil {
		t.Errorf("second Migrate: %v, want nil — a migrated database must still boot", err)
	}
}

// The DSN arrives from DATABASE_URL, and an operator who mistypes it should be
// told so at boot rather than discover a half-applied schema.
func TestMigrateRefusesAConnectionStringItCannotParse(t *testing.T) {
	if err := postgres.Migrate("postgres://user:pass@%%%/db"); err == nil {
		t.Error("Migrate accepted a connection string that is not a URL")
	}
}

// A libpq keyword/value DSN is a form pgxpool.ParseConfig accepts as readily as
// a URL, so the pool opens on one and nothing upstream objects. url.Parse
// accepts it too — it simply does not mean anything, and overwriting the scheme
// of a value that has none yields pgx5://host=localhost%20port=5432, which
// fails much later inside the migrator with an error naming none of this.
//
// So the form is refused here, where the reason is still known, and the message
// says what is wanted without echoing the string that carries the password.
func TestMigrateRefusesAConnectionStringThatIsNotAURL(t *testing.T) {
	err := postgres.Migrate("host=localhost port=5432 user=app password=s3cret dbname=discovery")
	if err == nil {
		t.Fatal("Migrate accepted a keyword/value DSN")
	}
	if !strings.Contains(err.Error(), "postgres://") {
		t.Errorf("error %v does not tell the operator which form is required", err)
	}
	if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "user=app") {
		t.Errorf("error %v echoes the connection string, which carries the password", err)
	}
}
