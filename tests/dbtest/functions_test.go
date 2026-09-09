package dbtest_test

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// clock is a TIME literal or, when nil, SQL NULL. Spelled as a pointer rather
// than a zero value because "00:00" and "no window set" are different answers
// and midnight is a real opening time.
func clock(literal string) *string {
	if literal == "" {
		return nil
	}
	return &literal
}

// The daily half of validity, against the real database rather than the Go
// twin, because this is the only gate clause whose wrong answer is a row that
// is never returned and never logged. A catalog that quietly stops matching at
// 22:00 produces no error anywhere.
func TestWithinDailyWindow(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	cases := []struct {
		name           string
		from, to, when string
		want           bool
	}{
		{"inside a forward window", "09:00", "17:00", "12:00", true},
		{"before a forward window", "09:00", "17:00", "08:59", false},
		{"after a forward window", "09:00", "17:00", "17:01", false},
		{"on the opening bound", "09:00", "17:00", "09:00", true},
		{"on the closing bound", "09:00", "17:00", "17:00", true},

		// The case every hand-written BETWEEN gets wrong: from > to, so
		// `t BETWEEN from AND to` is false for every t and the shop is shut
		// around the clock.
		{"inside the evening half of a wrap-around", "22:00", "02:00", "23:30", true},
		{"inside the morning half of a wrap-around", "22:00", "02:00", "01:00", true},
		{"in the closed middle of a wrap-around", "22:00", "02:00", "12:00", false},
		{"on the opening bound of a wrap-around", "22:00", "02:00", "22:00", true},
		{"on the closing bound of a wrap-around", "22:00", "02:00", "02:00", true},

		// No daily window set is OPEN, not closed. STRICT here would return
		// NULL, the gate clause would fail, and every catalog that never
		// published opening hours would vanish from discover.
		{"both bounds absent", "", "", "03:00", true},
		{"only the opening bound set", "09:00", "", "03:00", true},
		{"only the closing bound set", "", "17:00", "23:00", true},

		// from == to is the degenerate forward window: a single instant, not a
		// full day. It falls into the `from <= to` branch.
		{"at an instantaneous window", "09:00", "09:00", "09:00", true},
		{"outside an instantaneous window", "09:00", "09:00", "09:01", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var got bool
			err := pool.QueryRow(context.Background(),
				`SELECT within_daily_window($1::time, $2::time, $3::time)`,
				clock(testCase.from), clock(testCase.to), clock(testCase.when)).Scan(&got)
			if err != nil {
				t.Fatalf("within_daily_window(%q, %q, %q): %v",
					testCase.from, testCase.to, testCase.when, err)
			}
			if got != testCase.want {
				t.Errorf("within_daily_window(%q, %q, %q) = %v, want %v",
					testCase.from, testCase.to, testCase.when, got, testCase.want)
			}
		})
	}
}

// A NULL coordinate must produce NULL, not a distance. Without STRICT this body
// does something worse than return zero: `least()` IGNORES NULLs, so
// `least(1, NULL)` is 1, `asin(1)` is pi/2, and the function confidently returns
// half the Earth's circumference for a coordinate it could not read — a number
// that passes every "is this a plausible distance" check a caller might make.
func TestHaversineOfAMissingCoordinateIsNullRatherThanHalfTheEarth(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	var distance *float64
	err := pool.QueryRow(context.Background(),
		`SELECT geo_haversine_m(NULL::double precision, 77.64, 12.97, 77.64)`).Scan(&distance)
	if err != nil {
		t.Fatalf("geo_haversine_m: %v", err)
	}
	if distance != nil {
		t.Errorf("a NULL latitude returned %v metres; STRICT is what makes this NULL", *distance)
	}
}

// geo_distance_m returns NULL for every geometry that is not a Point, and the
// call site guards on the type so that NULL is never compared. The function
// keeps returning NULL because that is honest; this pins that it does.
func TestDistanceToANonPointGeometryIsNull(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	var distance *float64
	err := pool.QueryRow(context.Background(),
		`SELECT geo_distance_m('{"type":"Polygon","coordinates":[]}'::jsonb, 12.97, 77.64)`).Scan(&distance)
	if err != nil {
		t.Fatalf("geo_distance_m: %v", err)
	}
	if distance != nil {
		t.Errorf("a Polygon returned %v metres", *distance)
	}
}

// GeoJSON is [longitude, latitude], the reverse of every argument list in the
// migration. Swapping them puts Bengaluru in Somalia, and both values stay in
// range so nothing rejects it. Asserted against a point one degree of latitude
// north: ~111 km, where the swapped reading would give a number three times
// larger.
func TestDistanceReadsGeoJSONAsLongitudeThenLatitude(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	var distance float64
	err := pool.QueryRow(context.Background(),
		`SELECT geo_distance_m('{"type":"Point","coordinates":[77.64,13.97]}'::jsonb, 12.97, 77.64)`).
		Scan(&distance)
	if err != nil {
		t.Fatalf("geo_distance_m: %v", err)
	}
	if distance < 110_000 || distance > 112_000 {
		t.Errorf("one degree of latitude measured %.0f m, want ~111 km — the coordinate "+
			"order is probably reversed", distance)
	}
}

// The plan claims geo_haversine_m does NOT inline, as a consequence of STRICT
// over a non-strict `least()` body, rather than as something anybody measured.
// This is the measurement. If a later PostgreSQL inlines it after all, this test
// is what reports the good news — the cost is a function call per candidate row
// on the Point-to-Point refinement path, and nobody would otherwise notice it
// going away.
//
// Read off EXPLAIN (VERBOSE)'s Output line: an inlined SQL function is replaced
// by its body, so the function's own name disappears from the plan.
func TestHaversineDoesNotInline(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	seedCatalog(t, pool, "inline-probe")
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO resource_geometries
		   (catalog_id, resource_id, target_path, source_path, geojson,
		    cells_full, cells_cover, min_lat, max_lat, min_lon, max_lon)
		 VALUES ('inline-probe', NULL, '$.provider.locations[*].gps',
		         '$.provider.locations[0].gps',
		         '{"type":"Point","coordinates":[77.64,12.97]}',
		         '{}'::bigint[], ARRAY[1::bigint], 12.97, 12.97, 77.64, 77.64)`); err != nil {
		t.Fatalf("seed a geometry: %v", err)
	}

	// Column arguments, not literals: an IMMUTABLE function over constants is
	// folded at plan time, and a folded call proves nothing about inlining.
	plan := dbtest.ExplainVerbose(t, pool,
		`SELECT geo_haversine_m(min_lat, min_lon, 12.97, 77.64) FROM resource_geometries`)

	if !strings.Contains(plan, "geo_haversine_m") {
		t.Errorf("geo_haversine_m inlined after all — good news, and the plan's note that "+
			"STRICT over a least() body prevents it is now wrong:\n%s", plan)
	}
}

// geo_distance_m is deliberately NOT marked STRICT so that it CAN inline: a
// CASE body is non-strict, and PostgreSQL declines to inline a STRICT SQL
// function whose body is non-strict. Marking it STRICT would buy nothing — a
// NULL geom already falls through the CASE to NULL — and would cost a function
// call per candidate row. This is that asymmetry, asserted rather than assumed.
func TestDistanceDoesInline(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	plan := dbtest.ExplainVerbose(t, pool,
		`SELECT geo_distance_m(geojson, min_lat, min_lon) FROM resource_geometries`)

	if strings.Contains(plan, "geo_distance_m") {
		t.Errorf("geo_distance_m did not inline; it is NOT STRICT precisely so that it "+
			"can, and a call per candidate row is what that buys:\n%s", plan)
	}
}

// The Go twin and the SQL original must agree, because both are consulted about
// the same search: `geo_distance_m` refines a Point candidate inside the query
// and `geo.HaversineM` is what the walk and the bounds use in Go. A
// disagreement is not a rounding curiosity — it is a result 10.1 km from a 10 km
// search, returned by one side and refused by the other, with no error anywhere.
//
// The epsilon is tight on purpose. These are the same expressions in the same
// order over the same IEEE doubles, so anything beyond a few ULPs means the two
// bodies have actually diverged and not merely rounded differently.
func TestHaversineAgreesWithItsGoTwin(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	cases := []struct {
		name     string
		from, to domain.GeoPoint
	}{
		{"a city block", domain.GeoPoint{Lat: 12.9716, Lon: 77.5946},
			domain.GeoPoint{Lat: 12.9750, Lon: 77.5980}},
		{"across a country", domain.GeoPoint{Lat: 12.9716, Lon: 77.5946},
			domain.GeoPoint{Lat: 28.6139, Lon: 77.2090}},
		{"across the equator", domain.GeoPoint{Lat: 12.9716, Lon: 77.5946},
			domain.GeoPoint{Lat: -33.8688, Lon: 151.2093}},

		// Both sides of the seam, where a naive delta of 359 degrees is the
		// classic wrong answer.
		{"over the antimeridian", domain.GeoPoint{Lat: 1.0, Lon: 179.9},
			domain.GeoPoint{Lat: 1.0, Lon: -179.9}},

		{"pole to pole", domain.GeoPoint{Lat: 90, Lon: 0}, domain.GeoPoint{Lat: -90, Lon: 0}},

		// The clamp's own case: floating-point overshoot puts asin's argument
		// past 1 for antipodal points, which is NaN in Go and an error in SQL.
		{"antipodal", domain.GeoPoint{Lat: 12.9716, Lon: 77.5946},
			domain.GeoPoint{Lat: -12.9716, Lon: -102.4054}},

		{"the same point twice", domain.GeoPoint{Lat: 12.9716, Lon: 77.5946},
			domain.GeoPoint{Lat: 12.9716, Lon: 77.5946}},
		{"a southern pair", domain.GeoPoint{Lat: -34.6037, Lon: -58.3816},
			domain.GeoPoint{Lat: -22.9068, Lon: -43.1729}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var got float64
			err := pool.QueryRow(context.Background(),
				`SELECT geo_haversine_m($1::double precision, $2::double precision,
				                        $3::double precision, $4::double precision)`,
				testCase.from.Lat, testCase.from.Lon, testCase.to.Lat, testCase.to.Lon).Scan(&got)
			if err != nil {
				t.Fatalf("geo_haversine_m: %v", err)
			}

			want := geo.HaversineM(testCase.from, testCase.to)
			if math.Abs(got-want) > 1e-6+1e-9*math.Abs(want) {
				t.Errorf("SQL says %.9f m and Go says %.9f m — the two bodies have diverged",
					got, want)
			}
		})
	}
}
