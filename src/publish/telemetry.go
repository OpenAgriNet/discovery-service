package publish

import (
	"context"
	"slices"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The publish path's contribution to the span: one request_info event, recorded
// through fact rather than onto a span directly (A23), because
// tests/architecture/boundary_test.go refuses the SDK import here. Every name
// below comes from the registry.

// observePublish records the shape of a publish, at intake.
//
// BEFORE the A1 MASTER refusal, deliberately: after it, publish.catalog_types
// would read REGULAR on every span that exists — a table saying nobody ever
// tried. Whether the volume counts and publish.visible_to belong on a span at
// all is a judgement call decided in opentelemetry.md.
func observePublish(ctx context.Context, envelope beckn.Context, action beckn.CatalogPublishAction) {
	resources, offers, validity := volume(action.Catalogs)

	fact.ObserveInt64(ctx, fact.PublishCatalogCount, int64(len(action.Catalogs)))
	fact.ObserveInt64(ctx, fact.PublishResourceCount, resources)
	fact.ObserveInt64(ctx, fact.PublishOfferCount, offers)
	fact.ObserveBool(ctx, fact.PublishValidityPresent, validity)

	fact.ObserveStrings(ctx, fact.PublishProviderIDs, publisherIDs(action.Catalogs))

	types, modes, networks := directives(action, envelope.NetworkID)
	fact.ObserveStrings(ctx, fact.PublishCatalogTypes, types)
	fact.ObserveStrings(ctx, fact.PublishUpdateModes, modes)
	fact.ObserveStrings(ctx, fact.PublishVisibleTo, networks)
}

// volume totals the payload and reports whether ANY catalog set a validity
// window — the freshness question it serves is answered yes by one of them.
func volume(catalogs []beckn.Catalog) (resources, offers int64, validity bool) {
	for _, catalog := range catalogs {
		resources += int64(len(catalog.Resources))
		offers += int64(len(catalog.Offers))
		validity = validity || len(catalog.Validity) > 0
	}
	return resources, offers, validity
}

// publisherIDs is the DISTINCT provider-node ids that sent this. Same name stem
// as the discover path's providerIDs on purpose: both read the same field.
//
// Not bounded here — PublishProviderIDs carries MaxEntries and the record clamps
// against it, so the limit is stated once, on the Definition.
func publisherIDs(catalogs []beckn.Catalog) []string {
	ids := make([]string, 0, len(catalogs))
	for _, catalog := range catalogs {
		if catalog.BppID != "" && !slices.Contains(ids, catalog.BppID) {
			ids = append(ids, catalog.BppID)
		}
	}
	return ids
}

// directives reads the three per-catalog instructions off the request, RESOLVED
// rather than as sent (A9 field-wise, C9 never from content, C8 the request's
// own network).
//
// Through the write path's OWN applyDirectiveDefaults rather than a local copy:
// a second implementation would be a second place for A9 to drift, and the drift
// would be invisible — the span would say MERGE while the row was replaced. The
// loop walks the CATALOGS and looks the directive up, so a catalog with no
// directive at all resolves the same way as one naming only catalogId.
func directives(
	action beckn.CatalogPublishAction, network string,
) (types, modes, networks []string) {
	for _, catalog := range action.Catalogs {
		sent, _ := directiveFor(action, catalog.ID)
		directive := applyDirectiveDefaults(sent, catalog.ID, network)

		types = add(types, directive.CatalogType)
		modes = add(modes, directive.UpdateMode)
		for _, visible := range directive.VisibleTo {
			networks = add(networks, visible)
		}
	}
	return types, modes, networks
}

// add appends unless the value is already there or is empty. These are SETS over
// the request: twenty catalogs published MERGE is one mode, not twenty.
func add(values []string, value string) []string {
	if value == "" || slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
