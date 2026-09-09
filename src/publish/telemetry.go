package publish

import (
	"context"
	"slices"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The publish path's contribution to the span: one request_info event, and
// Trace projects it.
//
// Through fact rather than onto a span directly, and A23 is why: a controller
// that linked the OpenTelemetry SDK would drag it into src/domain and
// src/storage behind it, and tests/architecture/boundary_test.go refuses the
// import. Every name below comes from the registry.

// observePublish records the shape of a publish, at intake.
//
// BEFORE the A1 MASTER refusal, deliberately. After it, publish.catalog_types
// would read REGULAR on every span that exists — a table saying nobody ever
// tried. At intake it answers who is trying to publish master data to a network
// that refuses it, and the error event on the same span carries the refusal.
//
// The counts are payload volume, and they are a judgement call recorded in
// opentelemetry.md rather than an obvious yes: together with publish.visible_to
// they reveal catalog size and distribution, which is arguably commercial
// information. In, because the spec asks for volume and OAN's providers are
// largely public bodies. On a network with competing commercial providers, drop
// both.
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

// volume totals the payload and reports whether any catalog set a validity
// window.
//
// Any rather than all: the attribute is a bool over a request that may carry
// many catalogs, and the freshness question it serves — did this publisher
// bother to say when their data expires — is answered yes by one of them.
func volume(catalogs []beckn.Catalog) (resources, offers int64, validity bool) {
	for _, catalog := range catalogs {
		resources += int64(len(catalog.Resources))
		offers += int64(len(catalog.Offers))
		validity = validity || len(catalog.Validity) > 0
	}
	return resources, offers, validity
}

// publisherIDs is the DISTINCT provider-node ids that sent this — who added the
// source.
//
// The same name stem as the discover path's providerIDs on purpose: both read
// the same struct field, so one concept keeps one name and the join between who
// published a thing and whose data answered a query is obvious rather than
// something a reader has to work out.
//
// Not bounded here. PublishProviderIDs carries MaxEntries and the record clamps
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

// directives reads the three per-catalog instructions off the request.
//
// Through the write path's OWN applyDirectiveDefaults rather than a local copy
// of the same three fallbacks, and that is the point: the attribute has to
// report the directive the write will act on. A second implementation would be
// a second place for A9's field-wise resolution to drift, and the drift would
// be invisible — the span would say MERGE while the row was replaced.
//
// Resolved rather than as-sent, because an absent directive and one naming only
// catalogId mean the same thing to a publisher (A9) and must not produce two
// different attribute values. A catalog with no directive at all resolves the
// same way, which is why the loop walks the CATALOGS and looks the directive up
// rather than walking the directives.
//
// The defaults are never inferred from content (C9): an absent catalogType is
// REGULAR even for a catalog carrying no offers, which is the reading the spec
// invites and the one that would make the A1 refusal reject every ordinary
// resource-only catalog.
//
// visibleTo resolves to the request's own network when omitted (C8), and the
// resolved set is what goes out because the question is which networks the data
// reached. An empty list would say it reached none, which is the one thing that
// did not happen.
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

// add appends unless the value is already there or is empty. Distinct because
// these are SETS over the request — twenty catalogs published MERGE is one
// mode, not twenty — and empty is dropped because a request with no networkId
// has no network to name, and "" is not one.
func add(values []string, value string) []string {
	if value == "" || slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
