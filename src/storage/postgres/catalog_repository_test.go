package postgres_test

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/storage/conformance"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// resolution is the H3 resolution these tests cover at — the same default
// config carries (GEO_RESOLUTION_CELLS=8). Spelled here rather than read from
// config, because a test that inherited the environment would cover at whatever
// the machine running it happened to export.
const resolution = 8

// This test file lives beside the adapter rather than under tests/, and that is
// forced rather than chosen: the import-graph guard bans tests/dbtest from
// importing src/storage/postgres, so a Postgres-backed conformance run has to
// sit on the side of the boundary that may import both.
func postgresBackends(t *testing.T) conformance.Backends {
	t.Helper()

	pool := dbtest.NewPostgres(t)
	return conformance.Backends{Catalogs: postgres.NewCatalogRepository(pool, resolution)}
}

// The whole point of the suite: the Postgres adapter answers the same cases the
// memory one does, from the same file, so a behaviour added for one is asserted
// on the other the same day.
func TestPostgresSatisfiesThePublishConformanceSuite(t *testing.T) {
	conformance.Run(t, postgresBackends, conformance.PublishCases())
}
