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

// migrationSource names the source driver in golang-migrate's own error text.
// It is not a path: the embedded filesystem has no path outside the binary, and
// a name that looked like one would send an operator hunting for a directory
// that is not on the disk.
const migrationSource = "embedded"

// Migrate applies every pending migration from the binary's embedded copy
// (D10), and is what DATABASE_AUTO_MIGRATE switches on.
//
// It takes no context, because golang-migrate's Up takes none either and a
// context parameter this function could only ignore would promise a
// cancellation that never happens. A migration is the one boot step that is
// genuinely not interruptible partway: the half of it that already ran is
// committed.
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

	// Both errors, and Close's two, are collected before any is returned: a
	// migration that ran against a connection it then failed to release is a
	// leak that the Up error would otherwise hide.
	upErr := instance.Up()
	if errors.Is(upErr, migrate.ErrNoChange) {
		// Not an error. AutoMigrate runs on every boot, and "already at the
		// latest version" is the answer on all but the first.
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
//
// The scheme is rewritten to pgx5 rather than required of the caller.
// DATABASE_URL is one value read by the pool and by this, the pool wants
// postgres:// and golang-migrate resolves its database driver by scheme, so
// asking an operator to supply a scheme that works for exactly one of the two
// readers is asking them to get it wrong.
func migrationTarget(dsn string) (string, error) {
	target, err := url.Parse(dsn)
	if err != nil {
		// The DSN itself is deliberately absent from the message: it carries
		// the database password, and a boot failure is the most-copied line in
		// any incident channel.
		return "", fmt.Errorf("parse the connection string: %w", err)
	}

	// pgxpool.ParseConfig accepts a libpq keyword/value DSN as readily as a URL,
	// so the pool opens on one and nothing upstream objects. url.Parse accepts
	// it too — it simply does not mean anything, and overwriting the scheme of a
	// value that has none produces pgx5://host=localhost%20password=s3cret...,
	// which golang-migrate then reports verbatim, password and all. That leak is
	// what this check exists to prevent; the confusing failure is only the
	// second reason.
	if target.Scheme != "postgres" && target.Scheme != "postgresql" {
		return "", errors.New("the connection string must be a postgres:// URL: golang-migrate " +
			"resolves its database driver by scheme, and a libpq keyword/value DSN has none")
	}
	target.Scheme = "pgx5"

	return target.String(), nil
}
