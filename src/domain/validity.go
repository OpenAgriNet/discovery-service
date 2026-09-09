// Package domain holds the catalog model, the query model, the validity rules
// and the merge-patch algebra — the shapes and decisions this service is about,
// with no I/O anywhere in them.
//
// Nothing here imports a driver, a protocol type or a logger, and
// purity_test.go fails the build if it starts to. That is the swap boundary
// TRD §5 asks for: every backend is written against these types, so replacing
// one is a new package under src/storage, not an edit that reaches in here.
package domain

// TimeOfDay is a wall-clock instant with no date — what a TimePeriod's
// startTime and endTime are.
//
// A plain value type rather than a time.Time, which would carry a date nobody
// supplied and whose zero value reads as midnight rather than as "no window".
// That is why every field holding one is a POINTER: nil is the absence,
// 00:00:00 is a real bound.
//
// Always UTC and already normalised. A bare 09:00:00 is interpreted in
// APP_DEFAULT_TIMEZONE and an offset form is converted, both in the publish
// mapper, so nothing downstream performs a timezone lookup per row.
type TimeOfDay struct {
	Hour   int
	Minute int
	Second int
}

// secondsSinceMidnight collapses the three fields to the one number every
// comparison in this file actually wants. Comparing the fields
// lexicographically would work and would be three chances to get the carry
// wrong.
func (t TimeOfDay) secondsSinceMidnight() int {
	const (
		secondsPerHour   = 3600
		secondsPerMinute = 60
	)
	return t.Hour*secondsPerHour + t.Minute*secondsPerMinute + t.Second
}

// WithinDailyWindow reports whether at falls inside the daily window [from, to],
// both bounds inclusive.
//
// This is the Go half of a rule that also exists in SQL as
// `within_daily_window`, and the two are held in agreement by Task 16's
// conformance fixtures. Nothing else may open-code the comparison: the
// midnight-wrap branch is the entire reason this function exists, and a second
// copy of it is a second place to omit it.
//
// A wrapping window — 22:00 to 02:00, where from > to — is the case a plain
// BETWEEN gets silently wrong. It is false for every instant of the day, so a
// shop open all night reads as a shop never open, and an absent search result
// is a failure nobody reports.
//
// A nil bound is unbounded on that side rather than a window of zero length;
// the schema admits either half of the pair alone. Both nil is no window at
// all, which is the commonest row in the corpus. A nil instant carries no
// information to judge against and therefore cannot close a window — the same
// reading of nil as everywhere else here.
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
