package postgres_test

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/src/storage/conformance"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// This test file lives beside the adapter rather than under tests/, and that is
// forced rather than chosen: the import-graph guard bans tests/dbtest from
// importing src/storage/postgres, so a Postgres-backed conformance run has to
// sit on the side of the boundary that may import both.
func postgresBackends(t *testing.T) conformance.Backends {
	t.Helper()

	pool := dbtest.NewPostgres(t)
	return conformance.Backends{
		Catalogs: postgres.NewCatalogRepository(pool, geo.DefaultTestResolution),

		// No embedder, which is what a Phase 1 deployment has (A5) and what
		// keeps this side answerable by the same fixtures the memory backend
		// answers: a semantic mode neither backend can run cannot make them
		// disagree.
		Search: postgres.NewSearchRepository(pool, searchConfig(), nil),
	}
}

// The whole point of the suite: the Postgres adapter answers the same cases the
// memory one does, from the same file, so a behaviour added for one is asserted
// on the other the same day.
func TestPostgresSatisfiesThePublishConformanceSuite(t *testing.T) {
	conformance.Run(t, postgresBackends, conformance.PublishCases())
}

// And the read half, from the same file the memory backend runs.
//
// This is where the two implementations of every rule that exists twice —
// within_daily_window and domain.WithinDailyWindow, geo_distance_m and
// geo.NearestGeometryM, the SQL cell algebra and geo.MatchesOp — are held to
// the same answer. Each pair agrees with itself by construction; only a fixture
// run through both can say they agree with each other.
func TestPostgresSatisfiesTheDiscoverConformanceSuite(t *testing.T) {
	conformance.Run(t, postgresBackends, conformance.DiscoverCases(geo.DefaultTestResolution))
}
