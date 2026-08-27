# Bharat Vistaar — Providers & External APIs

Network: **Bharat Vistaar** · domain `schemes:vistaar` · Beckn v1.1.0
Role: BPP (provider-side adapter) · **8 providers · 8 use cases · 16 external endpoints**

Detail lives in [`PROVIDERS.md`](./PROVIDERS.md).

---

## Providers — 8

| # | Provider | External API | Auth | Use case |
|---|---|---|---|---|
| 1 | **Agmarknet Vistaar** | `api.agmarknet.gov.in` — `/v1/fetch-agmarknet-vistaar-location` | token (query) | Daily mandi commodity prices near a location |
| 2 | **IMD** | `city.imd.gov.in` — weather by station id | none | Multi-day station weather forecast |
| 3 | **Mausamgram** | `mausamgram.imd.gov.in/nwpapi` — `/get-daily` | HTTP Basic | 5-day gram-panchayat weather forecast |
| 4 | **PM-Kisan** | `exlink.pmkisan.gov.in/.../chatbotservice.asmx` | AES envelope + token | PM-Kisan installment / beneficiary status |
| 5 | **PMFBY** | PMFBY REST — `/api/v1/policy/…`, `/api/v1/claims/…` | login → token | Crop-insurance policy & claim status |
| 6 | **Soil Health Card** | `soilhealth.dac.gov.in` — GraphQL | refresh → Bearer | Soil test results & fertilizer advice |
| 7 | **OAN vector index** | `3.6.146.174:8882/indexes/oan-index/search` | **none** | Knowledge advisory / document search |
| 8 | **Hasura** | internal GraphQL | admin secret | Agri scheme & ICAR advisory content |

Supporting (not a use-case backend): **Postgres/PostGIS** — resolves lat/lon to market
codes and weather stations for providers 1 and 2.

---

## Endpoints per provider — 16 total

| Provider | Endpoints |
|---|---|
| PMFBY | 6 — login, send OTP, verify OTP, farmer lookup, policy list, claim report |
| PM-Kisan | 4 — send OTP, verify OTP, user details, beneficiary status |
| Soil Health Card | 2 — token exchange, report fetch |
| Agmarknet · IMD · Mausamgram · vector index | 1 each |
| Hasura | many (internal store) |

---

## Use cases — 8

| Use case | Provider(s) | Beckn action |
|---|---|---|
| Mandi price discovery | Agmarknet + Postgres | `search` |
| Weather forecast (station) | IMD + Postgres | `search` |
| Weather forecast (panchayat) | Mausamgram | `search` |
| Knowledge advisory | OAN vector index | `search` |
| ICAR schemes | Hasura | `search` |
| PM-Kisan schemes + status | Hasura, then PM-Kisan | `search` → `init` → `status` |
| Crop insurance (PMFBY) | PMFBY | `init` → `status` → `search` |
| Soil Health Card | SHC GraphQL | `init` only |

Two use cases are **OTP-gated** and therefore multi-step (PM-Kisan, PMFBY).
One is **init-only** with no search path (Soil Health Card).

---

## Network endpoints

| Role | Production |
|---|---|
| BAP | `https://seeker-network-vistaar.da.gov.in` |
| BPP | `https://provider-network-vistaar.da.gov.in` |

---

## Caveat

Provider 1's endpoint above is the **production** one from live traces. The
`BharatVistaar` branch is 138 commits behind `main` and still calls the older
`/v1/fetch-agmarknet-vistaar` variant. See [`PROVIDERS.md` §7 gap 8](./PROVIDERS.md#correctness).
