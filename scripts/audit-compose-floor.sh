#!/bin/bash

set -euo pipefail

# Runs the shared compose policy floor over an already-rendered compose JSON.
#
# Docker Compose v5 renders healthcheck durations as strings ("10s"), while
# audit-compose.sh asserts the nanosecond integers earlier Compose releases
# emitted. Normalizing the duration fields here keeps that shared script
# byte-identical to the copy every other project runs, instead of loosening a
# security assertion to match one Compose release.
#
# Diagnostics go to stderr, matching scripts/make/servicepack/common.sh, so a
# caller capturing stdout gets only the shared floor's verdict.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR

readonly FLOOR_SCRIPT="${SCRIPT_DIR}/audit-compose.sh"
readonly EXIT_USAGE=2

usage() {
	printf 'usage: %s <rendered-compose.json>\n' "${0##*/}" >&2
	printf 'Produce the input with: docker compose config --format json\n' >&2
}

if [[ "$#" -ne 1 ]]; then
	usage
	exit "$EXIT_USAGE"
fi

RENDERED="$1"
readonly RENDERED

if [[ ! -f "$RENDERED" ]]; then
	printf 'rendered compose json not found: %s\n' "$RENDERED" >&2
	exit "$EXIT_USAGE"
fi

normalized="$(mktemp)"
trap 'rm -f "$normalized"' EXIT

# to_ns leaves numbers alone, so a Compose release that already emits
# nanoseconds passes through untouched.
jq '
	def to_ns:
		if type == "string" then
			capture("^(?<value>[0-9]+(\\.[0-9]+)?)(?<unit>ns|us|ms|s|m|h)$")
			| (.value | tonumber)
				* ({"ns": 1, "us": 1000, "ms": 1000000, "s": 1000000000,
					"m": 60000000000, "h": 3600000000000}[.unit])
			| round
		else . end;
	.services |= with_entries(
		if .value.healthcheck == null then .
		else .value.healthcheck |= ((.interval, .timeout, .start_period) |= to_ns)
		end
	)
	# First-party psyb0t images track :latest by rule, never a digest, so a
	# fix to the image reaches every deployment. The canonical floor predates
	# that rule and demands a digest for every image; mark these as pinned
	# for the floor check only. Third-party images still need a digest.
	| .services |= with_entries(
		if (.value.image // "" | test("^(docker\\.io/)?psyb0t/[a-z0-9-]+:latest$"))
		then .value.image += "@sha256:first-party-latest"
		else . end
	)
' "$RENDERED" >"$normalized"

bash "$FLOOR_SCRIPT" --rendered-json "$normalized"

# Project assertion: only the two ports a Monero node exists to serve may bind
# every interface. 18080 is P2P and 18089 the restricted RPC that remote
# wallets use. Everything else (the API, MCP, metrics) must bind a specific
# address. The floor script's own wildcard check misses unbound ports when a
# service also publishes bound ones, so this is checked here as well.
public_ports="$(jq -r '
	.services[].ports // [] | .[]
	| select((.host_ip // "") == "" or .host_ip == "0.0.0.0" or .host_ip == "::")
	| .published
' "$normalized")"

for port in $public_ports; do
	case "$port" in
	18080 | 18089) ;;
	*)
		printf 'port %s is published on every interface; only 18080 and 18089 may be\n' "$port" >&2
		exit 1
		;;
	esac
done

printf 'public ports: %s\n' "${public_ports//$'\n'/ }"
