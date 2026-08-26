package domain

import "testing"

// clock builds the instant a window is judged against. A helper rather than a
// literal at each call site, because every case in this file is about the
// relationship between three times and a struct literal buries it.
func clock(hour, minute, second int) *TimeOfDay {
	return &TimeOfDay{Hour: hour, Minute: minute, Second: second}
}

// A catalog with no daily window is open all day. Both bounds nil is by far the
// commonest row in the corpus — most publishers send startDate/endDate or
// nothing — so the branch that reads "no window" as "never open" would hide
// almost everything.
func TestNoDailyWindowIsAlwaysOpen(t *testing.T) {
	if !WithinDailyWindow(nil, nil, clock(3, 0, 0)) {
		t.Error("a catalog with no daily window was closed")
	}
}

func TestAForwardWindowIsOpenInsideIt(t *testing.T) {
	from, to := clock(9, 0, 0), clock(17, 0, 0)

	if !WithinDailyWindow(from, to, clock(12, 30, 0)) {
		t.Error("12:30 fell outside 09:00-17:00")
	}
}

func TestAForwardWindowIsClosedOutsideIt(t *testing.T) {
	from, to := clock(9, 0, 0), clock(17, 0, 0)

	for _, instant := range []*TimeOfDay{clock(8, 59, 59), clock(17, 0, 1), clock(3, 0, 0)} {
		if WithinDailyWindow(from, to, instant) {
			t.Errorf("%v fell inside 09:00-17:00", instant)
		}
	}
}

// Both bounds inclusive, matching the SQL BETWEEN the Go copy has to agree
// with. A half-open window here and a closed one in `within_daily_window` is
// one second a day where the two backends disagree — the kind of difference
// the conformance suite exists to catch and nobody would think to look for.
func TestAForwardWindowIncludesBothBounds(t *testing.T) {
	from, to := clock(9, 0, 0), clock(17, 0, 0)

	for _, instant := range []*TimeOfDay{from, to} {
		if !WithinDailyWindow(from, to, instant) {
			t.Errorf("%v is a bound of 09:00-17:00 and was excluded", instant)
		}
	}
}

// The pin from the plan (scenario 24). 22:00 -> 02:00 has from > to, and a
// BETWEEN is false for every instant of the day — a shop open all night reads
// as a shop never open, and nobody reports a result they never saw.
func TestAnOvernightWindowWrapsPastMidnight(t *testing.T) {
	from, to := clock(22, 0, 0), clock(2, 0, 0)

	for _, instant := range []*TimeOfDay{clock(23, 30, 0), clock(1, 0, 0), from, to} {
		if !WithinDailyWindow(from, to, instant) {
			t.Errorf("%v fell outside the overnight window 22:00-02:00", instant)
		}
	}
}

func TestAnOvernightWindowIsClosedInTheGap(t *testing.T) {
	from, to := clock(22, 0, 0), clock(2, 0, 0)

	for _, instant := range []*TimeOfDay{clock(2, 0, 1), clock(12, 0, 0), clock(21, 59, 59)} {
		if WithinDailyWindow(from, to, instant) {
			t.Errorf("%v fell inside the overnight window 22:00-02:00", instant)
		}
	}
}

// One bound absent is unbounded on that side, not a window of zero length. The
// schema admits either half of the pair on its own, so this is a shape a
// publisher can actually send.
func TestASingleBoundIsUnboundedOnTheOtherSide(t *testing.T) {
	opensAtNine := func(instant *TimeOfDay) bool { return WithinDailyWindow(clock(9, 0, 0), nil, instant) }
	shutsAtFive := func(instant *TimeOfDay) bool { return WithinDailyWindow(nil, clock(17, 0, 0), instant) }

	if !opensAtNine(clock(23, 0, 0)) || opensAtNine(clock(8, 0, 0)) {
		t.Error("a window with only a start bound did not read as open-ended")
	}
	if !shutsAtFive(clock(1, 0, 0)) || shutsAtFive(clock(18, 0, 0)) {
		t.Error("a window with only an end bound did not read as open-from-midnight")
	}
}

// from == to is neither forward nor wrapping, and the reading is the literal
// one: an inclusive range whose ends coincide is that single instant. Pinned
// because it is the case the SQL side must agree on and the one a reader is
// most likely to "fix" into meaning all day.
func TestAZeroLengthWindowIsThatInstantAlone(t *testing.T) {
	noon := clock(12, 0, 0)

	if !WithinDailyWindow(noon, noon, clock(12, 0, 0)) {
		t.Error("the instant a zero-length window names was excluded")
	}
	if WithinDailyWindow(noon, noon, clock(12, 0, 1)) {
		t.Error("a zero-length window was open a second later")
	}
}
