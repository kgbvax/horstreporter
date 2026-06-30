#!/bin/bash
#
# Launch the HorstReporter local operator agent (cmd/horstoperator-agent).
#
# The agent runs on the operator's machine, exposes a small local HTTP API
# (default 127.0.0.1:9955) that the browser opmode UI talks to directly, and
# bridges rotate/mode/status commands to a PSTrotator controller over UDP.
#
# Every setting can be overridden with an environment variable, e.g.:
#   STATION_LOCATOR=JN58td ./run_operator_agent.sh
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
STATION_LOCATOR="${STATION_LOCATOR:-JO32we}"   # required: Maidenhead locator (station position derived from it)

PST_HOST="${PST_HOST:-192.168.1.142}"
PST_PORT="${PST_PORT:-12000}"
PST_TIMEOUT_MS="${PST_TIMEOUT_MS:-1500}"

BACKEND_URL="${BACKEND_URL:-}"                 # optional reverse-proxy target
# Award "wanted" (DXCC/WAS/POTA) runs IN-PROCESS and LOCAL — the operator's log
# never leaves this machine. It activates when Wavelog is configured:
#   WAVELOG_API_KEY=…           (in .env; read-only key)
#   WAVELOG_STATION_ID=3427     (required: DCLNext get_contacts_adif needs it)
#   POTA_HUNTED_CSV=hunted.csv  (optional: enables POTA via your hunted-parks export)
# These are read from the environment / .env; no flag needed here.
CONTROL_PERMITTED="${CONTROL_PERMITTED:-true}" # set false for read-only/monitor
ALLOWED_MODES="${ALLOWED_MODES:-forward,backward,bidirectional}"
BEAMWIDTH_3DB_DEG="${BEAMWIDTH_3DB_DEG:-60}"

PST_LOG_TRAFFIC="${PST_LOG_TRAFFIC:-0}"        # set 1 to log UDP traffic
PST_LOG_TRAFFIC_HEX="${PST_LOG_TRAFFIC_HEX:-0}" # set 1 to add hex dumps

# UltraBeam RCU-06 beam-direction control over MQTT (separate from PSTrotator
# rotation). Disabled by default; set UB_ENABLED=true and a broker URL to turn on.
#   UB_PASSWORD comes from the environment / .env only (never a flag, never logged).
#   Over a plain tcp:// broker the password is sent in cleartext — use tls:// for
#   any non-localhost broker.
UB_ENABLED="${UB_ENABLED:-false}"
UB_BROKER_URL="${UB_BROKER_URL:-tcp://127.0.0.1:1883}"
UB_TOPIC_PREFIX="${UB_TOPIC_PREFIX:-ubctrl}"
UB_CLIENT_ID="${UB_CLIENT_ID:-}"               # empty = auto horstoperator-<nanos>
UB_USERNAME="${UB_USERNAME:-}"

# --- Build the argument list -------------------------------------------------
args=(
  -listen "$LISTEN"
  -station-name "$STATION_NAME"
  -station-locator "$STATION_LOCATOR"
  -pst-host "$PST_HOST"
  -pst-port "$PST_PORT"
  -pst-timeout-ms "$PST_TIMEOUT_MS"
  -control-permitted="$CONTROL_PERMITTED"
  -allowed-modes "$ALLOWED_MODES"
  -beamwidth-3db-deg "$BEAMWIDTH_3DB_DEG"
)

[ -n "$BACKEND_URL" ] && args+=(-backend-url "$BACKEND_URL")
[ "$PST_LOG_TRAFFIC" = "1" ] && args+=(-pst-log-traffic)
[ "$PST_LOG_TRAFFIC_HEX" = "1" ] && args+=(-pst-log-traffic-hex)

if [ "$UB_ENABLED" = "true" ] || [ "$UB_ENABLED" = "1" ]; then
  args+=(-ub-enabled -ub-broker-url "$UB_BROKER_URL" -ub-topic-prefix "$UB_TOPIC_PREFIX")
  [ -n "$UB_CLIENT_ID" ] && args+=(-ub-client-id "$UB_CLIENT_ID")
  [ -n "$UB_USERNAME" ] && args+=(-ub-username "$UB_USERNAME")
fi

# Append any extra flags the caller passed on the command line.
args+=("$@")

echo "Starting horstoperator-agent on ${LISTEN}"
echo "  PSTrotator : ${PST_HOST}:${PST_PORT} (reply port $((PST_PORT + 1)), timeout ${PST_TIMEOUT_MS}ms)"
echo "  Station    : ${STATION_NAME} @ ${STATION_LOCATOR}"
echo "  Control    : ${CONTROL_PERMITTED} (modes: ${ALLOWED_MODES})"
if [ "$UB_ENABLED" = "true" ] || [ "$UB_ENABLED" = "1" ]; then
  echo "  UltraBeam  : ${UB_BROKER_URL} (prefix: ${UB_TOPIC_PREFIX})"
fi

exec go run ./cmd/horstoperator-agent "${args[@]}"
