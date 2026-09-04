package middlewares

import "testing"

// A middleware mounted without RequestLogger above it finds no correlation in
// the context — correlationFrom returns nil — and record is written to
// tolerate that nil receiver rather than making every caller check first.
// recorded carries the same tolerance on the read side, for a request that
// never had anything recorded into it.
func TestRecordedOnANilCorrelationIsNilNotAPanic(t *testing.T) {
	var c *correlation

	if got := c.recorded(); got != nil {
		t.Errorf("recorded() on a nil correlation = %v, want nil", got)
	}
}
