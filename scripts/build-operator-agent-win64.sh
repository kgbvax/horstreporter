#!/bin/bash
#
# Cross-compile the HorstReporter local operator agent for 64-bit Windows.
#
# The agent has no cgo dependencies, so this is a pure Go cross-compile and
# runs from any host (macOS/Linux). Output is a standalone .exe the operator
# can run on a Windows shack PC alongside PSTrotator.
#
# Usage:
#   scripts/build-operator-agent-win64.sh                 # -> dist/horstoperator-agent-windows-amd64.exe
#   OUT=build/agent.exe scripts/build-operator-agent-win64.sh
#
set -euo pipefail

# Run from the repo root regardless of CWD so the module + sibling replaces resolve.
cd "$(dirname "$0")/.."

OUT="${OUT:-dist/horstoperator-agent-windows-amd64.exe}"
mkdir -p "$(dirname "$OUT")"

# GUI=1 (default) links with -H windowsgui so the tray build runs without a
# stray console window. Set GUI=0 to keep a console (handy for debugging the
# headless / -tray=false path where you want log output in a terminal).
GUI="${GUI:-1}"
LDFLAGS="-s -w"
if [ "$GUI" = "1" ]; then
  LDFLAGS="$LDFLAGS -H windowsgui"
fi

# Stamp the build with the current git revision if available (best-effort; the
# agent ignores it today, but it makes the produced binary traceable).
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo unknown)"

echo "Building horstoperator-agent for windows/amd64"
echo "  version : ${VERSION}"
echo "  output  : ${OUT}"
echo "  gui     : ${GUI} (windowsgui=${GUI})"

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build \
    -trimpath \
    -ldflags "$LDFLAGS" \
    -o "$OUT" \
    ./cmd/horstoperator-agent

echo "Done: ${OUT}"
ls -lh "$OUT"
