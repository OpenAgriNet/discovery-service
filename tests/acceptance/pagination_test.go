package acceptance

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
)

// fiveAtMajestic publishes five resources a spatial intent around Majestic
// matches, so a page size of two has to walk three pages to see them all.
func fiveAtMajestic(t *testing.T) *service {
	t.Helper()

	list := make([]map[string]any, 0, 5)
	for index := range 5 {
		list = append(list, aResource(fmt.Sprintf("r-%d", index), fmt.Sprintf("Lot %d", index)))
	}
	svc := newService(t)
	svc.publishCatalogs(t, aCatalog("c-lots", availableAt(majestic), resources(list...)))
	return svc
}

// pageAt asks for one page with limit and offset in the SIGNED body (A28), and
// returns the message so the scenario can read the pagination beside the
// catalogs.
func (s *service) pageAt(t *testing.T, limit, offset int) beckn.OnDiscoverAction {
	t.Helper()

	answer := s.post(t, "/discover", envelope(beckn.ActionDiscover, map[string]any{
		"intent": spatial(dwithin(providerGeoPath, majestic, 5000)),
		"limit":  limit,
		"offset": offset,
	}))
	if answer.status != http.StatusOK {
		t.Fatalf("POST /discover limit %d offset %d = %d, want 200 — the OAN profile "+
			"declares both on DiscoverAction\nbody: %s", limit, offset, answer.status, answer.body)
	}

	var body struct {
		Message beckn.OnDiscoverAction `json:"message"`
	}
	answer.decode(t, &body)
	return body.Message
}

// A caller walking pages in the body reaches every match exactly once, and
// every page says where it is: the page it applied, and a total that tells the
// last page from a short one. Before A28 the body could not carry the page at
// all — the schema refused the key — and the answer carried nothing but
// catalogs, so a short page and the end were the same thing.
func TestTheBodyPagesThroughEveryMatchAndTheTotalMarksTheEnd(t *testing.T) {
	svc := fiveAtMajestic(t)

	var seen []string
	for offset := 0; offset < 5; offset += 2 {
		message := svc.pageAt(t, 2, offset)

		page := message.Pagination
		if page == nil {
			t.Fatalf("offset %d: no pagination in the answer", offset)
		}
		if page.Limit != 2 || page.Offset != offset {
			t.Errorf("offset %d: pagination reports limit %d offset %d, want 2 and %d",
				offset, page.Limit, page.Offset, offset)
		}
		if page.Total == nil || *page.Total != 5 {
			t.Errorf("offset %d: total = %v, want 5", offset, page.Total)
		}
		seen = append(seen, resourceIDs(message.Catalogs)...)
	}

	slices.Sort(seen)
	if want := []string{"r-0", "r-1", "r-2", "r-3", "r-4"}; !slices.Equal(seen, want) {
		t.Errorf("walking the pages saw %v, want each of %v exactly once", seen, want)
	}
}

// The schema bounds the body's page, so a limit above its maximum is refused
// by L1 validation before the service clamps anything. The query string still
// clamps — it is outside the schema — which is one more reason it is the
// deprecated route.
func TestALimitAboveTheSchemaMaximumIsRefused(t *testing.T) {
	svc := fiveAtMajestic(t)

	answer := svc.post(t, "/discover", envelope(beckn.ActionDiscover, map[string]any{
		"intent": spatial(dwithin(providerGeoPath, majestic, 5000)),
		"limit":  101,
	}))
	if answer.status != http.StatusBadRequest {
		t.Fatalf("limit 101 answered %d, want 400\nbody: %s", answer.status, answer.body)
	}
	if code := answer.nack(t).Message.Error.Code; code != beckn.CodeSchemaValidationFailed {
		t.Errorf("the refusal is %s, want %s", code, beckn.CodeSchemaValidationFailed)
	}
}
