# ADR-0017 — Discover pagination: signed body, applied page, capped `total`

**Status:** Accepted
**Date:** 2026-10-27

## Context

0.1.0 read `limit` and `offset` from the query string, which the request
signature does not cover. The OAN profile of the spec moves them into
`DiscoverAction` and answers with `DiscoverPagination` (`total`, `limit`,
`offset`, all required), counted in resources rather than catalogs.

## Decision

1. **The page is read from the signed body.** `?limit=` or `?offset=` on
   `/discover` is refused with `SCH_INVALID_FORMAT`, naming
   `message.limit`/`message.offset`.
2. **The unit is the resource.** The page is a slice of ranked resource ids;
   a catalog may span pages.
3. **An oversize `limit` is clamped to `maxPageSize`; a page past the
   retrieval depth is refused.** `maxPageSize` may not sit below
   `defaultPageSize`, which config validation enforces.
4. **The response echoes the applied page**, not the requested one. `limit`
   is the page size applied, not the count on the page: a short last page
   still reports it.
5. **`total` is the ranked list's length, capped at
   `maxCandidatesPerMode`** — the last page a caller can fetch.
6. **The cap is applied to the fused list before the page is cut.**
   `maxCandidatesPerMode` counts ids *per retrieval mode*, so fusing several
   modes yields a list longer than any one of them returned. Slicing first and
   capping the count afterwards produces a last page carrying more resources
   than `total` leaves room for, and `offset + count == total` never becomes
   true. The storage layer caps, and the request layer reports what it is given.
7. **Order is stable across pages.** RRF breaks score ties by id.

## Alternatives considered

- **Ignore query-string pagination.** Answers the default page with a 200; a
  client walking offsets loops on page one with nothing telling it why.
- **Refuse an oversize `limit`.** The caller would get no results for a
  query that has them; clamping and echoing the applied value loses nothing.
- **Echo the requested page.** A caller that asked for 5000 and got 100 would
  compute its next offset from 5000 and skip results.
- **An exact `total` from a count query.** About 100x the cost of the search
  it accompanies, measured in A19, for a number no caller can page to.
- **Order by score alone.** Equal scores are common — every id only one mode
  returned, at the same rank — and two that swap between runs put one result
  on two pages and another on none.

## Consequences

- Breaking for 0.1.0 callers that paginate with the query string.
- `total` understates a broad query with more matches than the retrieval
  depth. The spec allows this: a CN must treat it as the end of what is
  pageable, not as an absolute match count.
- A page past the retrieval depth is a 400, not an empty page.
- `offset >= maxCandidatesPerMode` is the refusal test, not
  `offset + limit > maxCandidatesPerMode`. `pageOf` already truncates a page
  whose tail runs past the end, so testing the tail refuses the last page at
  every limit that does not divide the depth exactly, and the caller never
  reaches the `offset + count == total` that marks the end of the results.
