// Package migrations carries the schema's .up.sql/.down.sql pairs into the
// binary.
//
// The files live at the repository root rather than under src/storage/postgres
// because three different readers need them and only one of them is Go:
// `make migrate` and tests/dbtest both open the directory through golang-migrate's
// file:// source, and the Makefile's path is what an operator runs by hand. So
// the embed comes to the files. //go:embed cannot reach upward out of its own
// directory, which is why this file sits here and not beside the code that
// applies it.
//
// It holds no logic on purpose: src/storage/postgres/migrate.go is what runs
// these, and a package holding both the SQL and a database connection would be
// importable only by something willing to take the driver.
package migrations

import "embed"

// FS is every migration in this directory, at the root of the filesystem.
//
// The pattern excludes nothing by name — a new .sql pair is embedded by being
// added, and TestEveryMigrationOnDiskIsAlsoEmbedded is what says so rather than
// anyone remembering to check.
//
//go:embed *.sql
var FS embed.FS
