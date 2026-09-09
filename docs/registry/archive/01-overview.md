# Overview — architecture and topology

Two calls, two topologies, and why the second call is `select`.

*[Registry schema](02-registry-schema.md) · [Adding a provider](03-adding-a-provider.md) · [Use cases](usecases/README.md) · [docs home](README.md)*

---

## How it works

Two calls. The first finds **who**. The second gets the **data**.

**① `discover` → the network adapter, which answers immediately.** The adapter resolves
the request against the discovery service's indexed catalog store and returns
`on_discover` on the same connection. Nothing upstream is touched — no IMD, no
credential, no registry binding. It is a directory lookup, ~20ms, complete when it
returns.

What comes back is an **advertisement**, not data.
`network-specs/schema/examples/README.md` is explicit:

> Provider catalogs advertise stable capability metadata… **They do not contain the
> requested location, observation time, market, commodity, or current value.**

So `on_discover` says `mausamgram` serves `WeatherObservation` **`OnDemand`**. It does not
say whether it will rain in Nashik on Thursday. The `select` at ② returns the same
`@type` marked **`Direct`** — same resource type, now carrying the answer. That one field
is what tells the experience layer whether a second call is needed.

**② `select` → the provider-side adapter, which does the real work.** The experience
layer now names that provider and capability in the request body. The adapter resolves
the binding from the registry, enriches, maps the request, authenticates, calls the
upstream, and maps the answer back into Beckn.

**Where ② lands depends on the topology** — A and B below. The work itself is identical in
both, and it always happens on the provider side. Only the number of network hops in
front of it changes.

**Transport is synchronous** to start with: `/select` returns `on_select` on the same
connection. Sync vs async is not a topology choice, it is one config field —
`targetType: "url"` (sync `ReverseProxy`) or `"publisher"` (async, ONIX writes the ACK
itself). The spec is async-shaped, so sync is a **documented deviation**, not a feature.

---

## Which action is the second call?

**`select`.** Not a second `discover`: the authoritative spec forbids it.

`protocol-specifications-v2/api/v2.0.0/beckn.yaml` fixes the actors per action:
`discover` is **CN → DS only**. There is no read-only CN → PN action, so the lightest
door into a provider is `select`.

The payload shapes confirm it rather than merely permitting it. `DiscoverAction` carries
an `Intent`, which is `additionalProperties: false` over exactly `textSearch`, `filters`,
`spatial`, `mediaSearch` — **there is nowhere to put a location you are asking about, or
a date range.** `SelectAction` carries a `Contract`, whose
`commitments[].resources[].resourceAttributes` is an open `Attributes` container — and
that is the one slot every OpenAgriNet schema targets via `x-beckn-container`.

Reading `select` as a read is legitimate: `OnSelectAction` returns *"updated
consideration amounts and offer-linked pricing"* — a quote, not an order. For an
open-data provider the quote is zero-cost and the payload is the data. That is an
interpretation the whole network has to share (see [Open issues](reference/open-issues.md)).

> A copy of `beckn.yaml` vendored inside ONIX (`benchmarks/e2e/testdata/beckn.yaml`,
> 3379 lines vs the authoritative 4995) says `/discover` *"can be implemented by Catalog
> Discovery Services and **BPPs**."* **That clause does not exist in the authoritative
> document.** Validate against the authoritative file only.

The working example throughout is the **weather advisory**, served by `mausamgram` —
IMD's numerical weather prediction API. Two providers do not fit that shape: `pm-kisan`
and `pmfby` both make the farmer answer an OTP first, so one question spans several Beckn
actions carrying one `transaction_id` — `init` then `status` for PM-Kisan, `init` →
`status` → `discover` for PMFBY. Both are modelled rather than seeded, on the
[pm-kisan](usecases/pm-kisan.md) and [pmfby](usecases/pmfby.md) pages.

**Which actions those two flows should use is not settled.** `status` presumes a
`contract.id` this network never issued, and PMFBY's `discover` is CN → PN, which the rule
above makes DS-only. Question 8 of [Open issues](reference/open-issues.md) states the
conflict; the action is the third segment of every `bindingKey`, so it is worth settling
before onboarding.

---

## Topology A — one adapter, network layer

One ONIX instance handles both hops. It is simultaneously the consumer's outbound point
and the provider node.

```
experience layer ──▶ ONIX (network layer) ──▶ registry lookup ──▶ IMD
                 ◀──────── on_select ◀────────────────────────────
```

**How it works.** The experience layer posts `/discover`, gets catalogs back. It posts
`/select`; the same adapter resolves the binding, calls IMD, transforms, returns
`on_select` on the open connection.

Fewest moving parts: no signature verification against a third party, one registry
lookup, one process to operate. The cost is that BV is not really *on* a network — it is
a façade in Beckn's shape. Fine while BV is the only participant; it stops being fine the
moment a second BAP wants to call these capabilities.

**This is what the [Mausamgram page](usecases/mausamgram.md) traces**, and it is what BV
runs first.

---

## Topology B — adapter on both sides

The experience layer gets its own adapter, and so does the provider side.

```
experience layer
   │ POST /bap/caller/select                       plain HTTP, not yet signed
   ▼
CONSUMER ONIX — bapTxnCaller
   addRoute · sign                                 signs with bapId's key
   │ ReverseProxy, sync
   ▼
PROVIDER ONIX — bppTxnReceiver
   validateSign · validateVC · addRoute · validateSchema
   resolveCapability ──▶ registry ──▶ IMD          ← all the real work is here
   mapResponse · signAck
   └──── 200 on_select, back up both proxies ────▶
```

**How it works.** Same two hops, one more network boundary. Four things change:

1. **Signature verification becomes real.** `validateSign` does a live lookup on
   `context.bapId` for the consumer's public key. In topology A that resolves to BV
   itself and proves nothing; here it is the actual trust boundary. Note this is the
   *network* registry of subscriber keys — **not** the capability registry. Two
   different stores.
2. **Capability resolution stays on the provider side only.** The
   [registry](02-registry-schema.md) lookup and the `env://` credential pointers belong
   to the provider adapter and nowhere else. **The consumer side must never learn that
   `mausamgram` means `https://mausamgram.imd.gov.in/nwpapi`.** If it resolves
   capabilities it needs upstream secrets, which defeats the whole point.
3. **`bppTxnCaller` and `bapTxnReceiver` never fire.** They exist for the async callback,
   which sync does not use. Half the config is dormant — expected, not a
   misconfiguration.
4. **Latency is two proxy hops plus two registry lookups**, on top of the upstream call.

If both adapters are BV-operated and co-located, the network hop is a loopback to your
own port. That works, but `bapId` and `bppId` must resolve to genuinely different
subscriber records — otherwise `validateSign` is verifying a signature against the key
that produced it.

### What has to be built, either way

The custom work is the same in both topologies; only its host changes.

| step | status |
|---|---|
| `validateSign` `validateVC` `addRoute` `validateSchema` `signAck` | stock ONIX, unchanged |
| `resolveCapability` — registry lookup, rewrites route + body + auth header | **new**, a plain plugin. No fork. |
| `mapResponse` — upstream JSON → `on_select` | **new**, and needs a small ONIX core change |

Request-side steps are already pluggable: `initSteps` falls through to a plugin lookup
for any unrecognised name, and `validateVC` is wired exactly that way. **Response steps
are not.** The switch hard-codes `signAck` and `validateAckSign` as the only two that
reach `h.responseSteps`; any other name silently lands in the *request* chain, where it
never sees the upstream response. `mapResponse` therefore needs a generic
`plugins.responseSteps` list added to `initSteps` — roughly twenty lines. **That is an
ONIX gap, not a BV one, and it is worth upstreaming rather than carrying as a patch.**

Two traps worth knowing before writing either step:

- **A response step must never return an error to signal failure.** If `RunOnResponse`
  returns non-nil, `ReverseProxy`'s error handler writes a bare 502 and **discards the
  body** (`stdHandler.go:407-414`) — the caller gets no `error.code` at all. Rewrite
  `rctx.Body` and `rctx.StatusCode` into a signed NACK and return `nil`. Returning the
  error is the natural Go reflex and it is wrong here.
- **Don't reuse the stock `reqmapper` plugin.** It keys mappings by **action alone** from
  a `mappings.yaml` compiled at boot, one role per instance — it cannot express
  per-provider mappings. Reuse the JSONata engine (`jsonata.OpenLatest()`), not the
  plugin.

One related constraint that shapes both topologies: for v2 the router **ignores
`domain`** and keys purely on **version + endpoint**, with duplicate endpoints a hard
error. So `routingConfig` can express *one target per action for the whole adapter* — it
**cannot** express "`select` → mausamgram, `select` → agmarknet". That is precisely why
resolution is registry-backed rather than a bigger config file. The routing table gets
you to the provider backend; the registry gets you to the provider.
