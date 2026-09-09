# Stripped rationale

Staging, not a design document. The godoc sweep (#10) cut long explanatory
comments out of the source in favour of one-line godoc. Most of what it cut was
already written down — the A19 measurement, A14, A18's row-level disjunction, the
refused spatial operators, the daily-window wrap — and was simply a second copy.

What is below is the residue: reasoning that existed **only** in a comment and is
recorded here so a later design-doc pass can place it properly. When a fact from
here lands in `discover-and-publish.md` or an ADR, delete its section. When the
file is empty, delete the file.

Each entry names the symbol it came from, so `git log -S` finds the original
wording if the summary here loses something.

---

## 1. `ResourceKey` uses NUL as its separator, and nothing else is safe

`src/domain/key.go`

Catalog and resource ids are publisher-supplied text. Every printable separator —
`:`, `/`, `|` — appears in real ids, and an id containing the separator splits
into the wrong pair and hydrates a **different** resource, silently. PostgreSQL
rejects NUL inside a `TEXT` value outright, so no stored id can contain one; the
same property makes it unusable in JSON, which is where these ids arrive from.

The retrieval ports carry flat `[]string` rather than the pair because a
`Retriever` ranks and RRF fuses — both set operations over an opaque identity.
The pair is only needed again at hydration.

## 2. `TouchedSet` is a set beside a slice, deliberately, and the cost is measured

`src/domain/mergepatch.go`

`touched` crosses `DeriveFunc` and the repository as a slice and stays one: the
geometry clear and `PropagateGate` send it whole as a PostgreSQL array. Every
other consumer asks only for membership, once per resource in the merged catalog
— quadratic against a slice.

A full-mode publish touches every resource, so a 500-resource catalog costs
**~250k string comparisons per pass**, and `derive`, `coverGeometries` and
`writeResources` each make a pass. That is the whole reason both shapes exist.

## 3. A14's pointer has a specific silent failure mode

`src/domain/catalog_repository.go`

A14 says `DeriveFunc` takes `*Catalog`. What the comment recorded and the
amendment does not is *how a value parameter fails*: everything `derive` computes
it delivers by writing onto the merged catalog, and against a value parameter
that works for `merged.Resources[k].Field` — which reaches through the shared
backing array — and silently does not work for `merged.Geometries`, which is a
field assignment on a copy.

The result is a catalog stored with no catalog-level geometry rows, no error
anywhere, and every provider location unfindable.

## 4. `ErrRetrievalDepth` is the guard behind the guard

`src/domain/search_repository.go`

The plan makes the discover mapper the owner of this refusal, and the mapper
checks the same bound against the same `config.Search`, so a request over HTTP is
turned away before a query runs. The sentinel exists for the paths that do not
come through the mapper.

Both sentinels sit on the port rather than in the adapter that raises them for an
import-graph reason: `tests/architecture` forbids `src/discover` from importing
`src/storage/postgres`, so an error declared there is one the request path cannot
match — and an error it cannot match is an error it reports as a 500.

## 5. `PruneOfferReferences` is the only defence that catches a resource that never existed

`src/domain/gate.go`

Three defences cover dangling `resource_ids`. The delete-then-prune pair on a
FULL republish cannot distinguish a first-publish typo from a correct array; the
prune can, because it checks against the merged catalog. It also checks **every**
offer on the merged catalog, not only the ones the patch named — the invariant
worth holding is "every offer ends this transaction referencing resources that
exist", not "every offer we happened to look at".

`resource_ids` carries no foreign key because PostgreSQL cannot declare one into
an array, which is why any of this is needed.

## 6. `Capability.Ranked()` decides two things neither backend would remember

`src/domain/query.go`

Spatial and jsonpath are filters: they are carried by the predicate each ranked
mode already applies, so a backend satisfies them by running the search at all.
The split then decides

- that an **applied** filter is never reported in `X-Beckn-Degraded`, and
- that an intent naming only filters is still a query, rather than a request for
  nothing.

It lives in the package both backends import because a copy in each is a copy
that drifts, and the drift shows up as an empty page with nothing to explain it.
