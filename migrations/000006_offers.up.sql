CREATE TABLE offers (
    id           TEXT NOT NULL CHECK (id <> ''),
    catalog_id   TEXT NOT NULL REFERENCES catalogs (id) ON DELETE CASCADE,

    -- Which resources this offer applies to. EMPTY MEANS CATALOG-WIDE — an
    -- offer on everything the provider sells — so it is a meaningful state and
    -- not a default to be pruned into. See the FULL-republish rule below.
    resource_ids TEXT[]      NOT NULL DEFAULT '{}',

    -- The offer document, verbatim. This table got the rule right first and
    -- A17 is that rule applied to the other two: `catalogs.document` and
    -- `resources.document` are now the same thing for the same reasons.
    --
    -- There are no `descriptor` and `price` columns beside it. They would be
    -- `document->'descriptor'` and `document->'price'` copied out — the same
    -- bytes a second time, on a column already read in full by the one query
    -- that touches this table, indexed by nothing and filtered on by nothing.
    -- Unlike `resources.name`, which exists to carry a trigram index, they
    -- would pay for themselves nowhere and would drift the moment a republish
    -- updated the document and missed a projection. That is precisely what
    -- `catalogs.descriptor` and `resources.attributes` did.
    --
    -- Named `document` rather than `offer` because every table now spells it
    -- the same way, `OfferPatch.Document` in the domain already did, and
    -- `offer.offer` reads as a mistake at every call site.
    document     JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- An expired offer must not be returned. The catalog's own validity does
    -- not cover this: a live catalog routinely carries last month's offer.
    -- Offer.validity is the same TimePeriod, so it gets the same two pairs —
    -- a lunch special is a daily window and nothing else.
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    valid_time_from TIME,
    valid_time_to   TIME,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (catalog_id, id)
);

-- NO `idx_offers_catalog_id`, for the same reason: `PRIMARY KEY (catalog_id,
-- id)` already leads with catalog_id, and the delete-then-prune pair and the
-- hydration fetch are both catalog_id-prefix scans.

-- Hydration asks "which offers touch any of these resource ids" — an array
-- overlap (&&), which is a GIN scan.
CREATE INDEX idx_offers_resource_ids ON offers USING GIN (resource_ids);

-- For attribute filters rooted at the offer path (Task 22).
CREATE INDEX idx_offers_document ON offers USING GIN (document jsonb_path_ops);
