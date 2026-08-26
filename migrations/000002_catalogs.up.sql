CREATE TABLE catalogs (
    -- `CHECK (id <> '')` on every id column in this schema, for one reason
    -- that only surfaces two migrations later: `uq_resource_geometries` keys on
    -- `COALESCE(resource_id, '')`, so an empty-string resource id and a
    -- catalog-level geometry become the same key. The constraint lives on all
    -- four id columns rather than only that one, because "ids are never empty"
    -- is the invariant, and enforcing it in the one place that currently
    -- depends on it is how it stops being true somewhere else.
    id           TEXT PRIMARY KEY CHECK (id <> ''),

    -- Verbatim, and stored exactly once per catalog. A providers table would
    -- add a join to every read to save nothing: no query reaches a provider
    -- except through its catalog.
    -- DEFAULT so the lock-and-load INSERT that opens every publish can name
    -- `id` alone. See `updateMode` — MERGE and FULL.
    provider     JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- publishDirective.visibleTo: the network ids this catalog is discoverable
    -- from. An array because that is what the directive carries — a publisher
    -- naming two networks publishes into both from one call.
    --
    -- DEFAULT '{}' is a fail-safe, not a valid state: the writer fills an empty
    -- list with the request's network first, because a catalog visible to
    -- nobody is findable by nobody while reporting success.
    visible_to   TEXT[]      NOT NULL DEFAULT '{}',

    -- The publisher's own off switch (catalog.isActive). Withdrawing is not the
    -- same as narrowing.
    active       BOOLEAN     NOT NULL DEFAULT TRUE,

    -- catalog.validity is a TimePeriod, and a TimePeriod carries TWO windows
    -- that the spec's anyOf lets appear separately or together:
    --   startDate/endDate  a one-off calendar range   ("live Jan -> Mar")
    --   startTime/endTime  a window that REPEATS DAILY ("open 09:00 -> 17:00")
    -- They are independent, so they are two independent column pairs, and a
    -- row must satisfy both to be live. NULL means unbounded on that axis.
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    valid_time_from TIME,
    valid_time_to   TIME,

    -- published_at is set on first publish and never moves; updated_at moves on
    -- every republish. The upsert must set updated_at explicitly, because
    -- DEFAULT now() only fires on INSERT.
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
