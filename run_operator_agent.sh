#!/bin/bash
#
# Launch the HorstReporter local operator agent (cmd/horstoperator-agent).
#
# The agent runs on the operator's machine, exposes a small local HTTP API
# (default 127.0.0.1:9955) that the browser opmode UI talks to directly, and
# bridges rotate/mode/status commands to a PSTrotator controller over UDP.
#
# Every setting can be overridden with an environment variable, e.g.:
#   STATION_LAT=48.137 STATION_LNG=11.575 ./run_operator_agent.sh
#   PST_HOST=192.168.1.50 PST_PORT=12000 ./run_operator_agent.sh
#   PST_LOG_TRAFFIC=1 ./run_operator_agent.sh        # UDP TX/RX diagnostics
#
# Any extra flags are passed straight through to the agent:
#   ./run_operator_agent.sh -pst-timeout-ms 3000
#
# Secrets (Wavelog) come from the environment, not flags. The agent loads a
# .env file from its working directory (this repo root) if present, e.g.:
#   WAVELOG_API_KEY=xxxxxxxx
#   WAVELOG_URL=https://log.dclnext.darc.de/index.php   # optional; this is the default
# .env is git-ignored. For a systemd-managed agent use EnvironmentFile= instead
# of a CWD .env — see docs/deployment-secrets.md.
#
set -euo pipefail

# Run from the repo root so "go run ./cmd/..." resolves regardless of CWD
# (and so the agent finds a repo-root .env for WAVELOG_API_KEY etc.).
cd "$(dirname "$0")"

# --- Configuration (override via environment) --------------------------------
LISTEN="${LISTEN:-127.0.0.1:9955}"

STATION_NAME="${STATION_NAME:-operator-station}"
STATION_LAT="${STATION_LAT:-52.52}"            # default: Berlin (CLAUDE.md example)
STATION_LNG="${STATION_LNG:-13.40}"
STATION_LOCATOR="${STATION_LOCATOR:-}"         # optional Maidenhead locator

PST_HOST="${PST_HOST:-a6.kgbvax.net}"
PST_PORT="${PST_PORT:-12000}"
PST_TIMEOUT_MS="${PST_TIMEOUT_MS:-1500}"

BACKEND_URL="${BACKEND_URL:-}"                 # optional reverse-proxy target
# horstawards runs on the server (kgbvax.net); the agent reaches it via the
# backend's /horstawards reverse-proxy. Set to enable WAS/POTA "wanted", e.g.
#   HORSTAWARDS_URL=https://horstreporter.kgbvax.net/horstawards
HORSTAWARDS_URL="${HORSTAWARDS_URL:-}"
CONTROL_PERMITTED="${CONTROL_PERMITTED:-true}" # set false for read-only/monitor
ALLOWED_MODES="${ALLOWED_MODES:-forward,backward,bidirectional}"
BEAMWIDTH_3DB_DEG="${BEAMWIDTH_3DB_DEG:-60}"

PST_LOG_TRAFFIC="${PST_LOG_TRAFFIC:-0}"        # set 1 to log UDP traffic
PST_LOG_TRAFFIC_HEX="${PST_LOG_TRAFFIC_HEX:-0}" # set 1 to add hex dumps

# --- Build the argument list -------------------------------------------------
args=(
  -listen "$LISTEN"
  -station-name "$STATION_NAME"
  -station-lat "$STATION_LAT"
  -station-lng "$STATION_LNG"
  -pst-host "$PST_HOST"
  -pst-port "$PST_PORT"
  -pst-timeout-ms "$PST_TIMEOUT_MS"
  -control-permitted="$CONTROL_PERMITTED"
  -allowed-modes "$ALLOWED_MODES"
  -beamwidth-3db-deg "$BEAMWIDTH_3DB_DEG"
)

[ -n "$STATION_LOCATOR" ] && args+=(-station-locator "$STATION_LOCATOR")
[ -n "$BACKEND_URL" ] && args+=(-backend-url "$BACKEND_URL")
[ -n "$HORSTAWARDS_URL" ] && args+=(-horstawards-url "$HORSTAWARDS_URL")
[ "$PST_LOG_TRAFFIC" = "1" ] && args+=(-pst-log-traffic)
[ "$PST_LOG_TRAFFIC_HEX" = "1" ] && args+=(-pst-log-traffic-hex)

# Append any extra flags the caller passed on the command line.
args+=("$@")

echo "Starting horstoperator-agent on ${LISTEN}"
echo "  PSTrotator : ${PST_HOST}:${PST_PORT} (reply port $((PST_PORT + 1)), timeout ${PST_TIMEOUT_MS}ms)"
echo "  Station    : ${STATION_NAME} @ ${STATION_LAT},${STATION_LNG}"
echo "  Control    : ${CONTROL_PERMITTED} (modes: ${ALLOWED_MODES})"

exec go run ./cmd/horstoperator-agent "${args[@]}"
