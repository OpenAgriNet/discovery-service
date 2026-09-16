package postgres

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // the pgx5:// target
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/OpenAgriNet/discovery-service/migrations"
)

// migrationSource names the source driver in golang-migrate's error text. Not a
// path: the embedded filesystem has none, and a name that looked like one would
// send an operator hunting for a directory that is not on the disk.
const migrationSource = "embedded"

// Migrate applies every pending migration from the binary's embedded copy
// (D10), and is what DATABASE_AUTO_MIGRATE switches on.
//
// No context parameter: golang-migrate's Up takes none, so one here would
// promise a cancellation that never happens.
func Migrate(dsn string) error {
	target, err := migrationTarget(dsn)
	if err != nil {
		return err
	}

	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("open the embedded migrations: %w", err)
	}

	instance, err := migrate.NewWithSourceInstance(migrationSource, source, target)
	if err != nil {
		return fmt.Errorf("open the migrator: %w", err)
	}

	// Up's error and Close's two are collected before any is returned: a
	// connection the migrator failed to release is a leak the Up error would
	// otherwise hide.
	upErr := instance.Up()
	if errors.Is(upErr, migrate.ErrNoChange) {
		// Not an error: this runs on every boot, and "already at the latest
		// version" is the answer on all but the first.
		upErr = nil
	}
	if upErr != nil {
		upErr = fmt.Errorf("apply migrations: %w", upErr)
	}

	sourceErr, databaseErr := instance.Close()
	if sourceErr != nil {
		sourceErr = fmt.Errorf("close the migration source: %w", sourceErr)
	}
	if databaseErr != nil {
		databaseErr = fmt.Errorf("close the migration database: %w", databaseErr)
	}
	return errors.Join(upErr, sourceErr, databaseErr)
}

// migrationTarget translates DATABASE_URL into the URL golang-migrate wants.
// The scheme is rewritten to pgx5 rather than required of the caller: one value
// serves the pool, which wants postgres://, and golang-migrate, which resolves
// its driver by scheme.
func migrationTarget(dsn string) (string, error) {
	target, err := url.Parse(dsn)
	if err != nil {
		// The DSN stays out of the message: it carries the password, and a boot
		// failure is the most-copied line in any incident channel.
		return "", fmt.Errorf("parse the connection string: %w", err)
	}

	// The check prevents a password LEAK, not merely a confusing failure. pgx
	// accepts a libpq keyword/value DSN and url.Parse does not reject one, so
	// overwriting the scheme of a value that has none yields
	// pgx5://host=localhost%20password=s3cret..., which golang-migrate then
	// reports verbatim.
	if target.Scheme != "postgres" && target.Scheme != "postgresql" {
		return "", errors.New("the connection string must be a postgres:// URL: golang-migrate " +
			"resolves its database driver by scheme, and a libpq keyword/value DSN has none")
	}
	target.Scheme = "pgx5"

	return target.String(), nil
}
