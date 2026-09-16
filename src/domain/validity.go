// Package domain holds the catalog model, the query model, the validity rules
// and the merge-patch algebra, with no I/O anywhere in them.
//
// Nothing here imports a driver, a protocol type or a logger, and
// purity_test.go fails the build if it starts to — the swap boundary TRD §5
// asks for.
package domain

// TimeOfDay is a wall-clock instant with no date — what a TimePeriod's
// startTime and endTime are.
//
// Always UTC and already normalised by the publish mapper. Every field holding
// one is a pointer: nil is absence, 00:00:00 is a real bound.
type TimeOfDay struct {
	Hour   int
	Minute int
	Second int
}

func (t TimeOfDay) secondsSinceMidnight() int {
	const (
		secondsPerHour   = 3600
		secondsPerMinute = 60
	)
	return t.Hour*secondsPerHour + t.Minute*secondsPerMinute + t.Second
}

// WithinDailyWindow reports whether at falls inside the daily window
// [from, to], both bounds inclusive. A nil bound is unbounded on that side; a
// nil instant cannot close a window.
//
// Wrapping windows — 22:00 to 02:00, where from > to — are the case a plain
// BETWEEN gets silently wrong, and the reason nothing else may open-code this
// comparison. The SQL half is `within_daily_window`, held in agreement with
// this one by Task 16's conformance fixtures.
func WithinDailyWindow(from, to, at *TimeOfDay) bool {
	if at == nil || (from == nil && to == nil) {
		return true
	}

	instant := at.secondsSinceMidnight()
	switch {
	case from == nil:
		return instant <= to.secondsSinceMidnight()
	case to == nil:
		return instant >= from.secondsSinceMidnight()
	}

	start, end := from.secondsSinceMidnight(), to.secondsSinceMidnight()
	if start > end {
		return instant >= start || instant <= end
	}
	return instant >= start && instant <= end
}
