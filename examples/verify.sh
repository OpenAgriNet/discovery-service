#!/usr/bin/env bash
#
# End-to-end check of the three retrieval paths against a running stack.
#
#   make run && examples/verify.sh
#
# Publishes examples/01 and then asserts, for each discover example, the EXACT
# set of resource ids that comes back. Exact rather than "at least one": a
# filter that has quietly stopped filtering still returns rows, and a
# subset assertion passes for it. The whole point of two spatial cases and two
# jsonpath cases below is that each one EXCLUDES something the others include —
# if the predicates were being ignored, every case would return all three
# resources and the differences are what catch it.
#
# Every response is also checked as an envelope: the HTTP status, the callback
# action name, and the transaction/message ids echoed back from the request.
# A 200 carrying a NACK body is a real failure mode and it is what `status`
# alone would miss.
set -u -o pipefail

BASE="${BASE:-http://localhost:8080}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

pass=0
fail=0
red=$'\033[31m'; green=$'\033[32m'; dim=$'\033[2m'; off=$'\033[0m'

ok()   { pass=$((pass + 1)); printf '  %sPASS%s %s\n' "$green" "$off" "$1"; }
bad()  { fail=$((fail + 1)); printf '  %sFAIL%s %s\n' "$red" "$off" "$1"; }

# post <path> <file> -> body on stdout; headers and status land in files.
#
# Files rather than variables because every call site is a command
# substitution, and a subshell's assignments do not survive it — a `STATUS`
# set here would read as empty in the caller and every status assertion would
# compare against "".
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
HDRS="$WORK/headers"
post() {
  local path="$1" file="$2" body
  body="$(curl -sS -X POST "$BASE$path" \
            -H 'Content-Type: application/json' \
            --data-binary @"$file" \
            -D "$HDRS" -w '\n%{http_code}')"
  printf '%s' "${body##*$'\n'}" > "$WORK/status"
  printf '%s' "${body%$'\n'*}"
}
status() { cat "$WORK/status"; }

# jq is not assumed: python3 is already a dependency of this repo's tooling
# and every developer box that can run the test suite has it.
py() { python3 -c "$1" "${@:2}"; }

check_envelope() {
  local label="$1" request="$2" response="$3" want_action="$4" want_status="$5"
  local got
  got="$(py '
import json, sys
req = json.load(open(sys.argv[1]))
res = json.loads(sys.argv[2])
ctx = res.get("context") or {}
msg = res.get("message") or {}
problems = []
if ctx.get("action") != sys.argv[3]:
    problems.append("action=%r want %r" % (ctx.get("action"), sys.argv[3]))
for field in ("transactionId", "messageId"):
    if ctx.get(field) != req["context"][field]:
        problems.append("%s not echoed (%r)" % (field, ctx.get(field)))
if ctx.get("version") != "2.0.0":
    problems.append("version=%r" % ctx.get("version"))
if msg.get("status") == "NACK":
    problems.append("NACK: %s" % json.dumps(msg.get("error")))
print("; ".join(problems))
' "$request" "$response" "$want_action")"

  if [ "$(status)" != "$want_status" ]; then
    bad "$label — HTTP $(status), want $want_status"
    return
  fi
  if [ -n "$got" ]; then
    bad "$label — $got"
    return
  fi
  ok "$label"
}

# resources <response> -> sorted, comma-joined resource ids across all catalogs
resources() {
  py '
import json, sys
res = json.loads(sys.argv[1])
ids = sorted(r["id"]
             for c in (res.get("message") or {}).get("catalogs", [])
             for r in c.get("resources", []))
print(",".join(ids))
' "$1"
}

offers() {
  py '
import json, sys
res = json.loads(sys.argv[1])
ids = sorted(o["id"]
             for c in (res.get("message") or {}).get("catalogs", [])
             for o in c.get("offers", []))
print(",".join(ids))
' "$1"
}

# discover_case <file> <label> <expected resource ids> [expected offer ids]
discover_case() {
  local file="$DIR/$1" label="$2" want_res="$3" want_off="${4:-}" body got
  printf '%s\n' "$label"
  body="$(post /discover "$file")"
  check_envelope "  envelope" "$file" "$body" "on_discover" "200"

  got="$(resources "$body")"
  if [ "$got" = "$want_res" ]; then
    ok "resources = [$got]"
  else
    bad "resources = [$got], want [$want_res]"
  fi

  if [ -n "$want_off" ]; then
    got="$(offers "$body")"
    if [ "$got" = "$want_off" ]; then
      ok "offers    = [$got]"
    else
      bad "offers    = [$got], want [$want_off]"
    fi
  fi

  if grep -qi '^x-beckn-degraded:' "$HDRS"; then
    printf '  %s%s%s\n' "$dim" "$(grep -i '^x-beckn-degraded:' "$HDRS" | tr -d '\r')" "$off"
  fi
  echo
}

VILLAGE=res-wx-village-belagavi
POINT=res-wx-point-dharwad
ALERT=res-wx-alert-statewide

printf '%s\n' "--- health"
for probe in healthz readyz; do
  code="$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/$probe")"
  [ "$code" = "200" ] && ok "GET /$probe -> 200" || bad "GET /$probe -> $code"
done
echo

printf '%s\n' "--- publish"
body="$(post /publish "$DIR/01-publish-weather-advisory.json")"
check_envelope "  envelope" "$DIR/01-publish-weather-advisory.json" "$body" "catalog/on_publish" "200"
got="$(py '
import json, sys
res = json.loads(sys.argv[1])
out = []
for r in res["message"]["results"]:
    out.append("%s=%s" % (r["catalogId"], r["status"]))
    if r.get("errors"):
        out.append(json.dumps(r["errors"]))
print(" ".join(out))
' "$body")"
if [ "$got" = "cat-ksndmc-weather-advisory=ACCEPTED" ]; then
  ok "$got"
else
  bad "$got"
fi
echo

# Publish is idempotent under MERGE, so re-running this script is safe and the
# second run asserts that too: same catalog, same three resources, no drift.

printf '%s\n' "=== TEXT SEARCH"

# "irrigation spraying advisory" is in the village and point long descriptions.
# The statewide alert resource says "Severe weather alerting" and must NOT
# match — it is the control that proves the lexical index is being consulted
# rather than every row being returned.
discover_case 02-discover-text-search.json \
  "02  textSearch 'irrigation spraying advisory' -> village + point, NOT the alert" \
  "$POINT,$VILLAGE"

# schemaContext is a Context field, not an Intent one. All three resources
# carry the same WeatherAdvisoryCapability @context, so all three match: this
# case pins that the filter ACCEPTS rather than that it discriminates.
discover_case 03-discover-schema-context.json \
  "03  schemaContext WeatherAdvisoryCapability -> all three" \
  "$ALERT,$POINT,$VILLAGE"

printf '%s\n' "=== SPATIAL"

# 25 km around (75.02, 15.47). The Dharwad Point sits 2 km away; the Belagavi
# Polygon contains that coordinate outright. The statewide alert carries only
# an ISO-3166-2 area CODE and no coordinates, so nothing was cell-indexed for
# it and it cannot be found spatially at all — by design, and this is the
# assertion that says so out loud.
discover_case 04-discover-spatial-dwithin.json \
  "04  S_DWITHIN 25km of Dharwad -> point + village, NEVER the code-only alert" \
  "$POINT,$VILLAGE"

# (74.50, 16.00) is deep inside the Belagavi polygon and ~120 km from the
# Dharwad point. Dropping the point here is what separates a working spatial
# predicate from one that matches the whole catalog: case 04 and case 05 have
# to disagree.
discover_case 05-discover-spatial-intersects.json \
  "05  S_INTERSECTS inside Belagavi only -> village alone" \
  "$VILLAGE" \
  "offer-wx-free-tier"

printf '%s\n' "=== ATTRIBUTE FILTER (jsonpath)"

# Rooted at the resource level.
discover_case 06-discover-filter-granularity.json \
  "06  geographicGranularity == Village -> village alone" \
  "$VILLAGE" \
  "offer-wx-free-tier"

# Rooted at the OFFER level and yet it narrows RESOURCES — this is the case
# that exercises A18's single composite filter_doc. Under the earlier
# three-column design an offer-rooted predicate could not select a resource at
# all, so if this returns all three, the composite has regressed.
discover_case 07-discover-filter-cross-level.json \
  "07  offers[*].descriptor.code == SUBSCRIPTION -> point alone (cross-level)" \
  "$POINT" \
  "offer-wx-subscription"

printf '%s\n' "=== REFUSALS"

# The same intent as 06 written WITHOUT the ? (...) filter. PostgreSQL runs it
# happily and `@?` answers true for every row, so the caller gets the entire
# corpus formatted as a filtered page and no error. A 400 here is the feature.
printf '%s\n' "08  jsonpath with no ?(...) -> 400 SCH_INVALID_JSONPATH"
body="$(post /discover "$DIR/08-discover-invalid-jsonpath.json")"
got="$(py '
import json, sys
res = json.loads(sys.argv[1])
err = (res.get("message") or {}).get("error") or {}
print("%s|%s" % (err.get("code"), (err.get("details") or {}).get("path")))
' "$body")"
if [ "$(status)" = "400" ] && [ "$got" = "SCH_INVALID_JSONPATH|\$.message.intent.filters.expression" ]; then
  ok "400 $got"
else
  bad "HTTP $(status) $got — want 400 SCH_INVALID_JSONPATH at \$.message.intent.filters.expression"
fi
echo

printf '%s\n' "--- pagination"
body="$(curl -sS -X POST "$BASE/discover?limit=1&offset=0" \
          -H 'Content-Type: application/json' \
          --data-binary @"$DIR/03-discover-schema-context.json")"
got="$(resources "$body")"
n="$(printf '%s' "$got" | awk -F, 'NF{print NF}')"
if [ "${n:-0}" = "1" ]; then
  ok "?limit=1 -> 1 resource [$got]"
else
  bad "?limit=1 -> ${n:-0} resources [$got]"
fi
echo

printf '%s\n' "================================"
printf '%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
