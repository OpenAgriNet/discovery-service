# Discovery Service runbook

> Applies to release **0.2.0**. How to understand, run, use, deploy, build and troubleshoot the Discovery Service, following one real example (IMD Mausamgram's on-demand weather forecast) from publish to discover.

## Contents

- [1. Introduction](#1-introduction)
- [2. Quickstart](#2-quickstart)
- [3. How-to guides](#3-how-to-guides)
  - [3.1 Publish resources](#31-publish-resources)
  - [3.2 Search resources](#32-search-resources)
  - [3.3 Use the API](#33-use-the-api)
  - [3.4 Deploy and configure](#34-deploy-and-configure)
  - [3.5 Build from source](#35-build-from-source)
  - [3.6 Troubleshoot](#36-troubleshoot)

## 1. Introduction

### What is the Discovery Service?

A farmer near Dharwad asks an app, *"Will it rain this week?"* Somewhere on the OpenAgriNet (OAN) network, a weather provider can answer that. But how does the app know which provider to ask?

The **Discovery Service** answers that question. It is the directory of an OAN network:

- **Providers** (weather agencies, seed suppliers, advisory services) **publish** the resources they offer.
- **Consumers** (farmer apps, AI assistants) **discover** the resources that match a farmer's question, place and time.

Once the app has found the right resource, it talks to that provider directly. The Discovery Service only helps them find each other.

### How it works

```mermaid
flowchart LR
    P(["Provider"]) -->|publish| A["Network adapter"]
    A -->|resource| DS["Discovery Service"]
    DS --> ST[("Storage")]
    C(["Consumer"]) -->|resource query| A2["Network adapter"]
    A2 -->|resource query| DS
    DS -.->|matching resources| A2
    OB["Observability: logs, traces and metrics across every step"]
```

| Part | What it does |
|---|---|
| **Provider** | Describes what it offers as resources and publishes them to the network. |
| **Consumer** | Asks the network for resources that match a farmer's need. |
| **Network adapter** | Checks that each message really comes from a trusted network member, then passes it on. |
| **Discovery Service** | Accepts published resources and answers resource queries, returning only what matches. |
| **Storage** | Keeps every published resource, ready to be searched quickly. |
| **Observability** | Records what happens at every step, so operators can see the service is healthy. |

### Use case: on-demand weather information

Let's follow one real resource from the provider to the farmer.

#### The schema

Every resource on OAN follows an agreed **schema**, so every app understands it the same way. Weather information uses [**WeatherObservation v0.1**](https://openagrinet.github.io/network-specs/schema/WeatherObservation/v0.1/).

The schema has two modes. In **on-demand** mode (`informationMode: OnDemand`), the resource doesn't carry the weather data itself. It describes what the provider *can* supply when asked: which measurements, which kind (observations or forecasts), at what level of detail, and for which area.

#### The provider

**IMD Mausamgram** is the India Meteorological Department's numerical weather prediction service. It can forecast the weather for any point in India, five days ahead. There are too many points to list one by one, so it publishes a **single on-demand resource** that says: *"ask me for any point in India, and I'll give you its forecast."*

#### The resource

Mausamgram's resource, following the WeatherObservation schema ([full publish request](https://github.com/OpenAgriNet/helmcharts/blob/81424a6e5928b282389fc20a3044cfc2967f6c7d/quick-start/capability-examples/WeatherObservation/publish/ondemand-mausamgram.json)):

```json
{
  "id": "resource:weather-observation:mausamgram:daily-forecast",
  "descriptor": {
    "code": "WEATHER_MAUSAMGRAM_DAILY_FORECAST",
    "name": "Five-day daily point weather forecast",
    "shortDesc": "Daily rainfall, temperature, humidity, wind and warning forecast for a requested coordinate.",
    "longDesc": "Invoke this Resource with a Point location to obtain one Direct WeatherObservation Resource per forecast day, each carrying that day's parameters. Values are bias-corrected model output, not station observations."
  },
  "resourceAttributes": {
    "@context": "https://openagrinet.github.io/network-specs/schema/WeatherObservation/v0.1/context.jsonld",
    "@type": "openagrinet:WeatherObservation",
    "informationMode": "OnDemand",
    "subjectCategories": ["Weather"],
    "languages": ["en"],
    "source": {
      "sourceId": "mausamgram",
      "sourceName": "IMD Mausamgram NWP",
      "sourceUri": "https://mausamgram.imd.gov.in/"
    },
    "supportedObservationTypes": ["Forecast"],
    "supportedParameters": ["Rainfall", "Temperature", "Humidity", "WindSpeed", "WindDirection", "Alert"],
    "geographicGranularities": ["Point"],
    "forecastHorizon": "P5D",
    "updateFrequency": "P1D",
    "coverageAreas": [
      {"codeScheme": "ISO-3166-1", "areaCode": "IN", "areaLevel": "Country", "areaName": "India"},
      {
        "type": "Polygon",
        "coordinates": [[[68.0, 6.5], [97.5, 6.5], [97.5, 37.5], [68.0, 37.5], [68.0, 6.5]]]
      }
    ]
  }
}
```

In plain words: *"Daily forecasts of rainfall, temperature, humidity, wind and weather warnings, up to five days ahead, refreshed daily, for any point you give me, covering India."* The polygon draws a box around India on the map, so the resource can be found by location.

#### `/publish`: the provider shares it

Mausamgram sends the resource to `POST /publish`. It doesn't limit who may see it, so it's **public**: every consumer on the network can find it. The Discovery Service checks it against the rules, prepares it for searching, stores it, and replies:

```
ACCEPTED: 1 resource published
```

#### `/discover`: the farmer app finds it

A farmer near Dharwad asks, *"Will it rain this week?"* The app turns that into a resource query to `POST /discover`:

```json
"intent": {
  "textSearch": "rainfall forecast",
  "spatial": [{ "op": "S_INTERSECTS", "geometry": { "type": "Point", "coordinates": [75.02, 15.47] }, "…": "…" }]
}
```

That is: *"rainfall forecasts covering the farmer's location."* Dharwad is inside Mausamgram's coverage area, so the Discovery Service answers with Mausamgram's daily forecast resource. A question about a place outside India, or asking for past observations instead of forecasts, would find nothing.

The app now knows Mausamgram can answer. It asks Mausamgram directly for the forecast at the farmer's point, and gets back one forecast per day.

## 2. Quickstart

In about 10 minutes you will run the Discovery Service on your own machine and play out the [weather use case](#use-case-on-demand-weather-information) from the Introduction: IMD Mausamgram publishes its on-demand forecast resource, and a farmer app near Dharwad finds it.

### What you need

- **Docker** with Compose v2
- **git**, **make**, **curl** and **jq**
- Ports **5432** and **8080** free

### 1. Get the code

```sh
git clone --branch 0.2.0 https://github.com/OpenAgriNet/discovery-service.git
cd discovery-service
```

### 2. Start the storage

```sh
make up
```

This starts the database the service keeps resources in, with local-only credentials.

### 3. Start the service

```sh
docker run -d --name discovery-service -p 8080:8080 \
  --add-host=host.docker.internal:host-gateway \
  -e DATABASE_URL='postgres://discovery:discovery@host.docker.internal:5432/discovery?sslmode=disable' \
  -e DATABASE_AUTO_MIGRATE=true \
  -e APP_NETWORK_ID=local-network \
  -e RATE_LIMIT_RPS=100000 -e RATE_LIMIT_BURST=100000 \
  -v "$PWD/tests/testdata/beckn-v2.0.0.yaml:/app/.cache/beckn/beckn.yaml:ro" \
  ghcr.io/openagrinet/discovery-service:0.2.0
```

| Setting | Why |
|---|---|
| `DATABASE_URL` | Where the storage is |
| `DATABASE_AUTO_MIGRATE=true` | Prepares the storage on first start |
| `APP_NETWORK_ID` | Required. The network this service serves. |
| `RATE_LIMIT_*` | Lifts the request limit so testing isn't throttled |
| The `-v` mount | Gives the service the protocol rulebook it checks requests against, so it starts even offline |

Check it's up:

```sh
curl -s localhost:8080/healthz    # {"status":"ok"}     the service is running
curl -s localhost:8080/readyz     # {"status":"ready"}  and can reach its storage
```

One start-up warning about the protocol rulebook in `docker logs discovery-service` is expected and harmless.

### 4. Publish the Mausamgram resource

Download Mausamgram's publish request ([view it](https://github.com/OpenAgriNet/helmcharts/blob/81424a6e5928b282389fc20a3044cfc2967f6c7d/quick-start/capability-examples/WeatherObservation/publish/ondemand-mausamgram.json)), then send it:

```sh
curl -fsSL --max-time 30 -o ondemand-mausamgram.json \
  https://raw.githubusercontent.com/OpenAgriNet/helmcharts/81424a6e5928b282389fc20a3044cfc2967f6c7d/quick-start/capability-examples/WeatherObservation/publish/ondemand-mausamgram.json

curl -s localhost:8080/publish -H 'Content-Type: application/json' \
  -d @ondemand-mausamgram.json | jq -c '.message.results[] | {status, stats}'
```

```json
{"status":"ACCEPTED","stats":{"itemCount":1,"providerCount":1,"categoryCount":1}}
```

`ACCEPTED` means the resource is stored and can be found. It's public, so every consumer can see it.

### 5. Discover it

To keep the commands short, define a helper that wraps a search in a standard request and prints the IDs of the resources found:

```sh
discover() {
  curl -s localhost:8080/discover -H 'Content-Type: application/json' -d '{
    "context": {
      "domain": "agriculture", "action": "discover", "version": "2.0.0",
      "transactionId": "7d2e0b63-0000-4000-8000-000000000001",
      "messageId": "7d2e0b63-0000-4000-9000-000000000001",
      "timestamp": "2026-11-11T06:31:00Z"
    },
    "message": { "intent": '"$1"' }
  }'
}
ids() { jq -c '[.. | .resources? | arrays | .[].id]'; }
```

**By text:** "rainfall forecast"

```sh
discover '{"textSearch": "rainfall forecast"}' | ids
# ["resource:weather-observation:mausamgram:daily-forecast"]
```

**By location:** a farmer near Dharwad, then someone in London

```sh
discover '{"spatial": [{"op": "S_INTERSECTS",
  "targets": "$.catalogs[*].resources[*].resourceAttributes.coverageAreas[*]",
  "geometry": {"type": "Point", "coordinates": [75.02, 15.47]}}]}' | ids
# ["resource:weather-observation:mausamgram:daily-forecast"]

discover '{"spatial": [{"op": "S_INTERSECTS",
  "targets": "$.catalogs[*].resources[*].resourceAttributes.coverageAreas[*]",
  "geometry": {"type": "Point", "coordinates": [-0.12, 51.5]}}]}' | ids
# []
```

Dharwad is inside Mausamgram's coverage area; London isn't.

**By attribute:** forecasts, then past observations

```sh
discover '{"filters": {"type": "jsonpath",
  "expression": "$.catalogs[*].resources[*] ? (@.resourceAttributes.supportedObservationTypes[*] == \"Forecast\")"}}' | ids
# ["resource:weather-observation:mausamgram:daily-forecast"]

discover '{"filters": {"type": "jsonpath",
  "expression": "$.catalogs[*].resources[*] ? (@.resourceAttributes.supportedObservationTypes[*] == \"Observation\")"}}' | ids
# []
```

Mausamgram offers forecasts only, so a search for observations doesn't find it.

**All together:** the farmer's question, *"rainfall forecasts for where I am"*

```sh
discover '{"textSearch": "rainfall forecast",
  "spatial": [{"op": "S_INTERSECTS",
    "targets": "$.catalogs[*].resources[*].resourceAttributes.coverageAreas[*]",
    "geometry": {"type": "Point", "coordinates": [75.02, 15.47]}}]}' | ids
# ["resource:weather-observation:mausamgram:daily-forecast"]
```

**Filter and location together:** the Mausamgram team's own sample search ([view it](https://github.com/OpenAgriNet/helmcharts/blob/81424a6e5928b282389fc20a3044cfc2967f6c7d/quick-start/capability-examples/WeatherObservation/discover/discover.json)), *"on-demand weather resources covering a point near Pune"*

```sh
curl -fsSL --max-time 30 -o discover-mausamgram.json \
  https://raw.githubusercontent.com/OpenAgriNet/helmcharts/81424a6e5928b282389fc20a3044cfc2967f6c7d/quick-start/capability-examples/WeatherObservation/discover/discover.json

curl -s localhost:8080/discover -H 'Content-Type: application/json' \
  -d @discover-mausamgram.json | ids
# ["resource:weather-observation:mausamgram:daily-forecast"]
```

Its filter asks for `subjectCategories` containing `Weather` **and** `informationMode` equal to `OnDemand`; its location asks for coverage of the point. Both must hold, and Mausamgram's resource meets both.

**A question the service refuses:** a filter with no test in it, which would match everything

```sh
discover '{"filters": {"type": "jsonpath", "expression": "$.catalogs[*].resources[*]"}}' | jq '.message.error.code'
# "SCH_INVALID_JSONPATH"
```

The service answers with an HTTP **400** naming the faulty field. It never returns a guess that looks like a real result.

> **Public and network-only resources.** Mausamgram's resource is public, so these searches don't name a network. A provider can instead limit its resources to one network (`visibleTo`, see [Publish resources](#choose-who-can-see-them)). Then a search finds them only if it names that network in `context.networkId`. A missing or wrong `networkId` is the most common reason for an empty result.

### 6. Clean up

```sh
docker rm -f discovery-service
make down     # stops the storage and deletes its data
```

## 3. How-to guides

Task-focused guides. Each one uses IMD Mausamgram's on-demand forecast resource from the [use case](#use-case-on-demand-weather-information).

| I want to… | Guide |
|---|---|
| Publish, update or hide resources | [Publish resources](#31-publish-resources) |
| Search by text, location or attribute | [Search resources](#32-search-resources) |
| Look up endpoints and error codes, or test with Postman | [Use the API](#33-use-the-api) |
| Run the service in a shared or production environment | [Deploy and configure](#34-deploy-and-configure) |
| Change the code or build my own image | [Build from source](#35-build-from-source) |
| Work out why something isn't working | [Troubleshoot](#36-troubleshoot) |

### 3.1 Publish resources

Providers publish through their network adapter to `POST /publish`. This guide follows IMD Mausamgram publishing its on-demand forecast resource. The full request is in the [Quickstart](#4-publish-the-mausamgram-resource).

#### Publish for the first time

A publish request has two parts:

- **`context`**: who is sending, to whom, when, and the action `catalog/publish`.
- **`message`**: the provider, its resources and offers, sent together as one **resource set** with its own `id` (here `catalog:weather-observation:mausamgram`), plus optional instructions on how to file it.

In outline:

```json
"message": {
  "catalogs": [{
    "id": "catalog:weather-observation:mausamgram",
    "descriptor": { "name": "IMD Mausamgram daily weather forecast", "…": "…" },
    "provider":  { "id": "mausamgram", "descriptor": { "name": "IMD Mausamgram" } },
    "resources": [ { "id": "resource:weather-observation:mausamgram:daily-forecast", "…": "…" } ]
  }]
}
```

- Each resource's details go in `resourceAttributes`, following its schema (here [WeatherObservation v0.1](https://openagrinet.github.io/network-specs/schema/WeatherObservation/v0.1/)). Everything in it becomes searchable.
- A resource set can also carry **offers**: the terms its resources are available under. Mausamgram's has none.
- One request can carry several resource sets.
- `publishDirectives` is optional. If you include one, it must name the set (`catalogId`) and its type (`catalogType: REGULAR`).

The reply has one result per resource set:

| Status | Meaning |
|---|---|
| `ACCEPTED` | Everything was stored |
| `PARTIAL` | Stored, except the parts listed in `errors` |
| `REJECTED` | Nothing was stored for this set; `errors` says why |

A badly formed request as a whole is refused with an HTTP 400 (a NACK), and nothing is stored.

#### Choose who can see them

`visibleTo` lists the networks whose searches may find the resources. Set it in `publishDirectives`:

```json
"publishDirectives": [
  { "catalogId": "catalog:weather-observation:mausamgram", "catalogType": "REGULAR", "visibleTo": ["local-network"] }
]
```


| `visibleTo` | Who finds them |
|---|---|
| left out | everyone (public), as with Mausamgram |
| `["local-network"]` | only searches naming `local-network` |
| `["n1", "n2"]` | searches naming `n1` or `n2` |

#### Add locations

Give locations as **GeoJSON** (points, lines or polygons), anywhere in the resource. The service finds every shape on its own. In the example, Mausamgram's `coverageAreas` holds a polygon drawn around India.

A location given only as a code, such as Mausamgram's `{"areaCode": "IN", "areaName": "India"}`, **can't be searched by location** on its own. The resource is still found by text and filters. That's why Mausamgram includes the polygon as well.

Limits to know:
- Up to 256 shapes per resource set by default (`GEO_MAX_GEOMETRIES_PER_CATALOG`). Extras are reported in a `PARTIAL` result and aren't searchable.
- Very large areas (over about 6,000 km²) are matched more loosely, by the rectangle around them. Mausamgram's India-wide box is one, but since it's already a rectangle, nothing is lost.

#### Update them

By default a publish **merges** into what's stored (`updateMode: MERGE`). To change one resource, send the set with only that resource in it:
- fields you leave out stay as they were;
- a field set to `null` is deleted;
- other resources in the set are untouched.

`itemCount` in the reply counts the resources in *this* request, so this update reports `1`.

#### Replace them or remove resources

Set `updateMode: FULL` to replace the whole resource set with what you send. **Any resource you leave out is deleted.** This is how you remove resources.

#### Hide them or set when they're valid

- `isActive: false` hides the resources from every search without deleting them.
- Validity dates and daily time windows (for example, 06:00 to 18:00) limit when they can be found. Windows use the service's time zone, `Asia/Kolkata` by default.

There's no separate delete action at 0.2.0.

#### Things that are refused

| You send | Result |
|---|---|
| An offer naming a resource that doesn't exist | that set fails with `BIZ_ITEM_NOT_FOUND` |
| The same set `id` twice in one request | the request is refused |
| `catalogType: MASTER`, or resources inheriting from others (`extends`) | refused with `SCH_TYPE_NOT_SUPPORTED` |
| A request over 10 MiB | HTTP 413; split it up |

### 3.2 Search resources

Consumers search through their network adapter with `POST /discover`. Every example here searches for IMD Mausamgram's on-demand forecast resource, as published in the [Quickstart](#2-quickstart).

#### The shape of a search

```json
{
  "context": {
    "domain": "agriculture",
    "action": "discover",
    "version": "2.0.0",
    "transactionId": "…", "messageId": "…", "timestamp": "2026-11-11T06:31:00Z"
  },
  "message": {
    "intent": { "textSearch": "rainfall forecast" },
    "limit": 20,
    "offset": 0
  }
}
```

- **`intent`** holds at least one of `textSearch`, `spatial` and `filters`. An empty intent is refused, because it would mean "everything".
- **`context.networkId`** (optional) says which networks to search:

| `networkId` | Searches |
|---|---|
| left out | public resources only, such as Mausamgram's |
| `"n1"` or `["n1"]` | resources visible to `n1`, **not** public ones |
| `["n1", "*"]` | both |

#### Search by text

```json
"intent": { "textSearch": "rainfall forecast" }
```

Finds Mausamgram's resource.
- **Any word can match**, and resources matching more words rank higher.
- **Small typos are tolerated** in resource names: `"Five-day pont wether forcast"` still finds it.
- Every text answer carries the header `X-Beckn-Degraded: semantic`. Searching by meaning is planned but not switched on at 0.2.0, so answers come from word matching. That's expected.

#### Search by location

Covering a farmer's location near Dharwad:

```json
"intent": {
  "spatial": [{
    "op": "S_INTERSECTS",
    "targets": "$.catalogs[*].resources[*].resourceAttributes.coverageAreas[*]",
    "geometry": { "type": "Point", "coordinates": [75.02, 15.47] }
  }]
}
```

Finds Mausamgram's resource, because Dharwad is inside its coverage area. The same search from London finds nothing.

| Part | Meaning |
|---|---|
| `op` | The question: `S_INTERSECTS` (overlaps or touches), `S_DWITHIN` (within a distance), `S_WITHIN`, `S_CONTAINS`, `S_OVERLAPS`, `S_EQUALS` or `S_DISJOINT` |
| `geometry` | Your point or area, in GeoJSON |
| `targets` | Which of the published shapes to compare against; here, each resource's `coverageAreas` |
| `distanceMeters` | For `S_DWITHIN` only; up to 200 km |

Only one location condition per search. `S_TOUCHES` and `S_CROSSES` aren't supported. Matching is slightly generous right at the edge of an area, so it never misses a real match.

#### Search by attribute

Any published field can be tested with a filter, with no setup. Resources that offer forecasts:

```json
"intent": {
  "filters": {
    "type": "jsonpath",
    "expression": "$.catalogs[*].resources[*] ? (@.resourceAttributes.supportedObservationTypes[*] == \"Forecast\")"
  }
}
```

Finds Mausamgram's resource. Asking for `"Observation"` instead finds nothing, because Mausamgram offers forecasts only.

- A filter must start at `$.catalogs` and contain a `? (...)` test. Without the test it would match everything, so it's refused.
- Values are exact: `"Forecast"` isn't `"forecast"`.
- Conditions can be joined with `&&`, for example `subjectCategories[*] == "Weather" && informationMode == "OnDemand"`.
- A filter can also test offers or the provider; it returns the resources they apply to.

#### Combine them

Put several conditions in one intent. Location, filter and schema conditions must **all** hold; text narrows and ranks within them.

The farmer's question, *"rainfall forecasts for where I am"*, is text plus location, and finds Mausamgram's resource. Move the point outside India and the same search finds nothing.

**Filter and location together.** This search, from the Mausamgram team's [sample request](https://github.com/OpenAgriNet/helmcharts/blob/81424a6e5928b282389fc20a3044cfc2967f6c7d/quick-start/capability-examples/WeatherObservation/discover/discover.json), asks for *on-demand weather resources covering a point near Pune*:

```json
"intent": {
  "filters": {
    "type": "jsonpath",
    "expression": "$.catalogs[*].resources[*] ? (@.resourceAttributes.subjectCategories[*] == \"Weather\" && @.resourceAttributes.informationMode == \"OnDemand\")"
  },
  "spatial": [{
    "op": "S_INTERSECTS",
    "targets": "$.catalogs[*].resources[*].resourceAttributes.coverageAreas[*]",
    "geometry": { "type": "Point", "coordinates": [74.4, 18.4] },
    "quantifier": "ANY"
  }]
}
```

- The **filter** keeps resources whose categories include `Weather` *and* whose mode is `OnDemand`.
- The **location** keeps resources whose coverage area contains the point. `quantifier: ANY` means one matching shape is enough (`ALL` and `NONE` are the alternatives).
- Mausamgram's resource passes both, so it's found. Change `OnDemand` to `Direct`, or move the point outside India, and it isn't.

If the conditions together match nothing, you get a normal empty answer (`200`, no resources).

To search only resources that follow a given schema, add `context.schemaContext`, for example the WeatherObservation schema.

#### Read the answer

The answer lists the matching resources, grouped by the set they were published in, each **exactly as published**, together with its provider (here IMD Mausamgram) and any offers that currently apply.

#### Page through results

- Send `limit` and `offset` **in the request body**, not the URL.
- `limit` defaults to 20, with a maximum of 100.
- You can page up to 500 results deep. `pagination.total` tells you how far you can page, not an exact count.

#### When a search is refused

A search the service can't answer exactly gets an HTTP **400** whose `details.path` points at the problem, never a widened answer. Common causes: a filter without a `? (...)` test, a radius over 200 km, two location conditions, or paging in the URL. All codes are in [Use the API](#error-codes).

### 3.3 Use the API

#### Endpoints

| Method and path | Beckn action → reply action | Purpose |
|---|---|---|
| `POST /publish` | `catalog/publish` → `catalog/on_publish` | Create, update or replace resources |
| `POST /discover` | `discover` → `on_discover` | Search published resources |
| `GET /healthz` | n/a | Is the process up? `200 {"status":"ok"}` |
| `GET /readyz` | n/a | Can it reach the database? `200 {"status":"ready"}` or `503 {"status":"unready"}` |

Every answer is **synchronous**: the result is in the HTTP response, and nothing is called back later.

#### Rules for every request

- A Beckn v2.0.0 envelope: `context` plus `message`, sent as `application/json`, up to 10 MiB.
- `context` needs `domain`, `action`, `version` (`2.0.0`), `transactionId`, `messageId` and `timestamp`.
- `context.action` must match the endpoint: a `discover` sent to `/publish` is refused.

The full machine-readable descriptions are [`docs/api/openapi.yaml`](api/openapi.yaml) and the Beckn specification every request is checked against, [`tests/testdata/beckn-v2.0.0.yaml`](../tests/testdata/beckn-v2.0.0.yaml). There's no SDK; it's plain HTTP and JSON.

#### Test with Postman

The repository ships a ready-made Postman collection: [`examples/OpenAgriNet-discovery-service.postman_collection.json`](../examples/OpenAgriNet-discovery-service.postman_collection.json). It holds 20 requests in four folders: Health, Publish, Discover and Refusals. It comes with its own sample data (a set of weather-advisory resources), separate from this guide's example.

1. Start a local stack with the [Quickstart](#2-quickstart).
2. Import the collection (**File → Import**). If the service isn't at `http://localhost:8080`, set the collection variable `baseUrl`.
3. Run **Publish → 01** first, which publishes the collection's sample resources. Every search looks for them.
4. Send any request, or run the whole collection with the **Collection Runner**.

Each request checks the exact resources it should return, so a broken filter fails rather than passing quietly. The Refusals requests are meant to get a 400.

From the command line, without installing Postman:

```sh
make newman
# or, against another deployment:
npx newman run examples/OpenAgriNet-discovery-service.postman_collection.json \
  --env-var baseUrl=https://discovery.example.org
```

The collection's sample resources are published to `local-network`, so on a shared deployment only searches naming `local-network` will see them.

#### Reading an error

A refused request returns a **NACK**:

```json
{
  "message": {
    "status": "NACK",
    "messageId": "b1000000-0000-4000-8000-000000000008",
    "error": {
      "code": "SCH_INVALID_JSONPATH",
      "message": "the expression has no ? (...) filter, so it selects rather than tests …",
      "details": { "path": "$.message.intent.filters.expression" }
    }
  }
}
```

- `messageId` matches the request.
- `details.path` points at the faulty part of **your request**.
- The header `X-Beckn-Error-Type` gives the category: `CONTEXT`, `CORE`, `DOMAIN`, `POLICY` or `SYSTEM`. A 429 also carries `Retry-After`.
- Publish problems inside one resource set aren't NACKs. They're listed in that set's `errors`, with status `PARTIAL` or `REJECTED`.

#### Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `CTX_MISSING_FIELD` / `CTX_INVALID_FIELD` | 400 | A `context` field is missing or malformed |
| `CTX_VERSION_UNSUPPORTED` | 400 | `version` isn't `2.0.0` |
| `CTX_ACTION_MISMATCH` | 400 | The action doesn't belong on this endpoint |
| `SCH_INVALID_JSON` | 400 | The body isn't valid JSON |
| `SCH_SCHEMA_VALIDATION_FAILED` | 400 | The body doesn't match the Beckn v2.0.0 schema, or repeats a resource-set `id` |
| `SCH_INVALID_FORMAT` | 400 | A value can't be used: unreadable shape, radius of zero or over the limit, two location conditions, paging past 500, or pagination in the URL |
| `SCH_INVALID_JSONPATH` | 400 | A filter or `targets` expression is unusable |
| `SCH_TYPE_NOT_SUPPORTED` | 400 | Valid, but not served here: `S_TOUCHES`, `S_CROSSES`, `catalogType: MASTER`, `extends`, or text search when it's switched off |
| `BIZ_ITEM_NOT_FOUND` | in a publish result | An offer names a resource the set doesn't contain |
| `POL_GENERIC_ERROR` | in a publish result | More shapes than the per-set limit |
| `POL_NP_CAPACITY_EXCEEDED` | 413 | The body is too large |
| `AUT_RATE_LIMITED` | 429 | Too many requests; wait for `Retry-After` |
| `NET_CATALOG_SOURCE_UNAVAILABLE` | 400 | A search method is unavailable and the deployment is set to refuse rather than answer partly |
| `NET_INTERNAL_ERROR` | 500 | An unexpected fault; check the service log for the request |

### 3.4 Deploy and configure

For network operators running the service in a shared or production environment. On Kubernetes, use the OAN Helm charts in [`OpenAgriNet/helmcharts`](https://github.com/OpenAgriNet/helmcharts); this page covers what the service itself needs.

#### What you need

| Need | Detail |
|---|---|
| **A database** | PostgreSQL 16 with the `vector` and `pg_trgm` extensions available (the image `pgvector/pgvector:0.8.0-pg16` has both). It's the only storage the service needs. |
| **The Beckn v2.0.0 specification** | Fetched at start-up, or mounted for offline clusters. The service won't start without it. |
| **A network adapter in front** | The service doesn't verify signatures, so it must only be reachable from the adapter |
| **The container image** | `ghcr.io/openagrinet/discovery-service:0.2.0`, for `amd64` and `arm64`. Listens on port 8080 and runs as a non-root user. |

#### Configure it

Settings come in four layers, each overriding the one before:

```
built-in defaults → config/common.yaml (shipped) → config/instance.yaml (yours, optional) → environment variables
```

- Put your non-secret settings in `instance.yaml`, starting from [`instance.yaml.example`](../config/instance.yaml.example).
- Put secrets, above all `DATABASE_URL`, in environment variables only.
- A misspelt or empty setting **stops the start-up** with a message naming it, so a typo can't be silently ignored.

##### The settings you'll most likely change

| Variable | Default | What it does |
|---|---|---|
| **`DATABASE_URL`** | required | The database connection. Environment only. |
| **`APP_NETWORK_ID`** | required | The network this deployment serves, for example `da.gov.in/vistaar` |
| `APP_DEFAULT_TIMEZONE` | `Asia/Kolkata` | Time zone for daily validity windows |
| `DATABASE_AUTO_MIGRATE` | `false` | Create or update the tables at start-up |
| `DATABASE_MAX_CONNS` | `32` | Database connections; see sizing below |
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | `20` / `40` | Requests per second per client address; see below |
| `GEO_MAX_GEOMETRIES_PER_CATALOG` | `256` | Location shapes indexed per resource set |
| `SERVER_MAX_REQUEST_BODY_BYTES` | 10 MiB | Largest accepted request |
| `SEARCH_MAX_PAGE_SIZE` | `100` | Largest page a search can ask for |
| `OTEL_EXPORTER` | `none` | Set to `otlp` to send traces and metrics |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

The remaining settings and their defaults are in the repository's [`config/common.yaml`](../config/common.yaml) and [`src/platform/config/config.go`](../src/platform/config/config.go).

#### Set up the database

The table definitions are built into the service. Either let it apply them at start-up (`DATABASE_AUTO_MIGRATE=true`, fine for one copy), or run `make migrate` as a deploy step (better when running several copies). The database role needs permission to create the extensions, or an administrator creates them first.

Back up the database: it holds everything that was published.

#### Run it offline

The service fetches the Beckn specification at start-up. For clusters without internet access, mount the repo's [`tests/testdata/beckn-v2.0.0.yaml`](../tests/testdata/beckn-v2.0.0.yaml) at `/app/.cache/beckn/beckn.yaml`, and leave telemetry off or point it at an internal collector. One start-up warning about the specification is then expected and harmless.

#### Size it

- **Database connections:** one search uses about three connections at once. Set `DATABASE_MAX_CONNS` to at least 3 × the searches you expect at the same time.
- **Capacity:** a search over 10,000 resources takes about 20 ms. A 4-CPU database handles about 90–100 searches a second. Scale the database, not the service.
- **Rate limit behind an adapter:** the limit counts per client address, and behind an adapter all traffic comes from one address. Set the limit to the **total** traffic you expect.
- **Several copies:** the service keeps no state, so you can run several. Each copy has its own rate limit.

#### Health checks and shutdown

| Probe | Endpoint | Fails when |
|---|---|---|
| Liveness | `GET /healthz` | the process isn't serving |
| Readiness | `GET /readyz` | the database doesn't answer within 2 s; recovers on its own |

Use HTTP probes (the image has no shell). On shutdown, requests in progress get up to 15 s to finish, so set the termination grace period above that.

#### Monitor it

- **Logs:** one JSON line per request, with its IDs, action, status, duration and any error code.
- **Traces and metrics:** set `OTEL_EXPORTER=otlp`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `APP_SUBSCRIBER_ID` and `APP_DOMAIN=Agriculture`. Details are in [`docs/telemetry.md`](telemetry.md).
- Search terms and published content are never recorded.

#### Keep it secure

- Make it reachable **only from the network adapter**, using a private network or network policy. Anyone who can reach `/publish` can publish.
- Terminate TLS at the ingress or adapter; the service speaks plain HTTP.
- Keep `AUTH_ENABLE_SIGNATURE_VERIFICATION=false` (the adapter verifies signatures).

#### Upgrade it

Pin the image to a version, read the [release notes](https://github.com/OpenAgriNet/discovery-service/releases) for the new version, apply migrations, then roll out.

### 3.5 Build from source

For contributors, and for anyone who needs their own binary or image.

#### What you need

| Tool | Why |
|---|---|
| **Go 1.26.10 or later** | Compile and test |
| **A C toolchain** (gcc or clang) | One of the libraries the service uses is written in C |
| **Docker** with Compose v2 | Local database, integration tests and image builds |
| **make**, **git** | Every workflow is a `make` target; run `make` alone to list them |

The other build tools (linter, migration tool, SQL code generator) are pinned in the repo and installed on first use.

#### Find your way around

| Folder | Holds |
|---|---|
| `src/publish/`, `src/discover/` | The two request paths |
| `src/domain/` | The core rules, with no database code |
| `src/storage/` | The storage code, and the test suite every storage option must pass |
| `src/indexing/` | Location indexing and the hook for future meaning-based search |
| `src/platform/` | Configuration, logging, errors, validation, telemetry |
| `migrations/` | The database tables |
| `examples/` | Sample requests and the Postman collection |
| `docs/adr/` | Architecture Decision Records |

How the parts fit together is in the [Introduction](#how-it-works).

#### Build and test

```sh
make build        # builds bin/discovery-service
make test         # every test, including database tests; the gate for every change
make test-short   # only the tests that don't need Docker
make lint         # code checks; must report 0 issues
```

Database tests start their own temporary database, so Docker must be running.

#### Run your build

```sh
make up           # starts the database only
make build
APP_NETWORK_ID=local-network \
DATABASE_AUTO_MIGRATE=true \
DATABASE_URL='postgres://discovery:discovery@localhost:5432/discovery?sslmode=disable' \
  ./bin/discovery-service
```

Then publish and search the Mausamgram resource exactly as in the [Quickstart](#4-publish-the-mausamgram-resource), and run `make verify` to check the repository's sample searches still return the right resources.

#### Change the database

- Queries are in `src/storage/postgres/queries/`. After editing, run `make sqlc` to regenerate the matching code.
- Tables are in `migrations/`. Apply with `make migrate`.

#### Build an image

The `Dockerfile` defaults to Docker Hardened Images, which need a Docker subscription (`docker login dhi.io`). Released images use them. Without a subscription, build on public images:

```sh
docker build \
  --build-arg BUILD_IMAGE=golang:1.27-alpine \
  --build-arg RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot \
  --build-arg INSTALL_BUILD_TOOLS=true \
  -t discovery-service:dev .
```

Run `discovery-service:dev` the same way the Quickstart runs the release image.

#### Extend it

- **Another datastore:** implement the two storage interfaces in `src/domain/` and pass the shared suite in `src/storage/conformance/`. The in-memory store is a working example.
- **A design change:** read the relevant ADR in [`docs/adr/`](adr/) first; it records what was considered and why.

#### Contribute

Before opening a pull request:

```sh
make lint && make test
```

CI checks the build, lint, tests, coverage of changed lines, generated code and a container vulnerability scan. Report bugs and propose changes through [GitHub issues](https://github.com/OpenAgriNet/discovery-service/issues).

### 3.6 Troubleshoot

For cluster or Helm problems, see [`OpenAgriNet/helmcharts`](https://github.com/OpenAgriNet/helmcharts).

**Look first at:**
- the error's `details.path`, which points at the faulty part of your request;
- the service's log line for your `messageId`;
- `/readyz`.

#### The service won't start

It stops with a message naming the problem.

| Message mentions | Fix |
|---|---|
| `APP_NETWORK_ID` or `DATABASE_URL` is required | Set it |
| `unknown key` or `empty value` | Fix that key in your `instance.yaml` |
| the Beckn spec can't be loaded | Allow internet access at start-up, or [mount the spec](#run-it-offline) |
| `enableSignatureVerification is true` | Set it back to `false`; the adapter verifies signatures |
| `otel.exporter is otlp` | Also set the endpoint, `APP_SUBSCRIBER_ID` and `APP_DOMAIN=Agriculture`, or turn telemetry off |
| database or migration errors | Check `DATABASE_URL`, and that the role can create the `vector` and `pg_trgm` extensions |

One warning about the Beckn specification followed by a normal start is fine.

#### `/readyz` returns 503

The database didn't answer within 2 s. Check it's running, reachable and not out of connections. Readiness recovers by itself.

#### Publishing went wrong

| Symptom | Fix |
|---|---|
| 400 `SCH_SCHEMA_VALIDATION_FAILED` | Fix the request at `details.path` to match Beckn v2.0.0 |
| 413 | The request is over 10 MiB. Split it, or raise `SERVER_MAX_REQUEST_BODY_BYTES`. |
| `PARTIAL` with `POL_GENERIC_ERROR` | Too many shapes; the extras aren't searchable. Raise `GEO_MAX_GEOMETRIES_PER_CATALOG`. |
| `PARTIAL` with `SCH_INVALID_FORMAT` | Fix the GeoJSON at the reported path |
| `BIZ_ITEM_NOT_FOUND` | An offer names a resource that doesn't exist |
| Resources disappeared | You published with `updateMode: FULL`, which replaces the whole resource set. Use the default `MERGE` to update part of it. |
| A field didn't change | Under `MERGE`, a field you leave out is kept. Send the new value, or `null` to delete it. |

#### A search returns nothing

Check in this order:

1. **Network.** If the resource was published to a network (`visibleTo`), does the search's `networkId` name it? With no `networkId`, only public resources, such as Mausamgram's, are searched. This is the most common cause.
2. **Active and valid.** Are the resources active, and is now inside their validity dates and daily window?
3. **Location.** Does the resource have a map shape? A location given only as a code (`"areaCode": "IN"`) never matches a location search; Mausamgram's resource works because it also has a polygon. Does `targets` point at where the shapes are (`coverageAreas`)?
4. **Combined conditions.** Location, filter and schema must *all* hold. Try each on its own.
5. **Filter values.** They're exact: `"Forecast"` isn't `"forecast"`.
6. **Was it really an error?** A 400 or 429 has no results either. Check the status code.

#### A search returns too much

- Any text word can match: `"rainfall forecast"` finds every resource that mentions rainfall *or* forecasts. Add a filter or location to narrow it.
- Location matching is slightly generous at boundaries (about 1 km), and very large areas are matched more loosely.

#### HTTP 400 on a search

The error code and `details.path` say what to fix; see [Use the API](#error-codes). The usual causes are pagination in the URL instead of the body, a filter without a `? (...)` test, and a radius over 200 km.

#### HTTP 429 Too Many Requests

The limit is per client address (20 a second by default). Behind an adapter, all traffic shares one address and so one limit. Raise `RATE_LIMIT_RPS` and `RATE_LIMIT_BURST` to the total traffic you expect.

#### Searches are slow

| Sign | Fix |
|---|---|
| `pgxpool_empty_acquire_total` rising | Not enough database connections. Raise `DATABASE_MAX_CONNS` to about 3 × concurrent searches. |
| Database CPU maxed out | Scale the database |
| Searches stopping at about 2 s | They hit the search time limit; narrow the query or scale the database |

#### Still stuck?

[Open an issue](https://github.com/OpenAgriNet/discovery-service/issues) with the release, the request (secrets removed), the response and the log line for its `messageId`.
