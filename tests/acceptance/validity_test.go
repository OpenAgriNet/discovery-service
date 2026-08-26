package acceptance

import (
	"slices"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
)

// dailyWindow is a validity carrying ONLY the clock half — no startDate, no
// endDate.
//
// That combination is the one worth an end-to-end scenario. A row with both
// halves is gated by the calendar bounds too, so a `within_daily_window` that
// had been dropped from the query would still look right; with the calendar
// half absent, the clock is the only thing standing between the catalog and the
// page.
func dailyWindow(from, to time.Time) func(map[string]any) {
	return withValidity(map[string]any{
		"startTime": clock(from),
		"endTime":   clock(to),
	})
}

// aShopAt is one catalog at majestic holding one resource, open on the window
// its options describe.
func aShopAt(t *testing.T, svc *service, id string, window func(map[string]any)) {
	t.Helper()

	results := svc.publishCatalogs(t, aCatalog(id,
		availableAt(majestic),
		resources(aResource("r-"+id, "wheat")),
		window,
	))
	if len(results) != 1 || results[0].Status != beckn.StatusAccepted {
		t.Fatalf("publish %s = %+v, want one ACCEPTED", id, results)
	}
}

// Scenario 23. A catalog with only a daily window is returned inside it and
// withheld outside it.
//
// Phrased relative to now rather than to a stated hour, and that is not a
// stylistic choice: the gate calls `now()` in SQL and never reads Scope.Now, so
// a scenario written as "open at 10:00" is a scenario that passes before lunch
// and fails after it.
//
// What it pins is the failure nobody reports. If the clock-only form were
// dropped on the way to storage — read as "no window", the way an absent
// validity is — every one of these catalogs would answer every search, and the
// only symptom would be a shop appearing at 03:00. The other direction, storing
// it and never testing it, hides a shop that should be open, and an absent
// result raises no complaint either.
func TestADailyWindowClosesTheCatalogOutOfHours(t *testing.T) {
	svc := newService(t)
	now := time.Now()

	aShopAt(t, svc, "open", dailyWindow(now.Add(-time.Hour), now.Add(time.Hour)))
	aShopAt(t, svc, "shut", dailyWindow(now.Add(2*time.Hour), now.Add(3*time.Hour)))

	page := svc.near(t, majestic, 5000)
	if got := resourceIDs(page); !slices.Equal(got, []string{"r-open"}) {
		t.Errorf("resources = %v, want [r-open]: r-shut opens in two hours", got)
	}
}

// Scenario 24. A window whose start is after its end wraps past midnight rather
// than being empty.
//
// Both catalogs here have `from > to`, which is what makes the pair worth
// running: a plain BETWEEN answers false for both, so a scenario using only the
// hidden one would pass against the bug. The instant sits in the GAP of the
// first and inside the WRAP of the second.
//
// The limit, stated rather than hidden: within two hours of midnight the
// arithmetic stops wrapping — now+1h or now-2h crosses the day boundary and one
// or both pairs degenerate into ordinary forward windows. The assertions still
// hold at every hour (a forward [now+1h, now-1h] excludes now just as the gap
// does, and a forward [now-1h, now-2h] cannot arise), but the wrapping BRANCH
// is not necessarily the one under test at 23:30. That branch belongs to Task
// 14's SQL unit test, which passes the instant to `within_daily_window` as an
// argument and can therefore choose its hour; this scenario is the end-to-end
// plumbing above it, and the split exists precisely because the gate calls
// `now()`.
func TestAnOvernightWindowWrapsPastMidnight(t *testing.T) {
	svc := newService(t)
	now := time.Now()

	aShopAt(t, svc, "gap", dailyWindow(now.Add(time.Hour), now.Add(-time.Hour)))
	aShopAt(t, svc, "wrap", dailyWindow(now.Add(-time.Hour), now.Add(-2*time.Hour)))

	page := svc.near(t, majestic, 5000)
	if got := resourceIDs(page); !slices.Equal(got, []string{"r-wrap"}) {
		t.Errorf("resources = %v, want [r-wrap]: now is in the gap of r-gap's window", got)
	}
}
