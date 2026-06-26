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

# Stamp the build with the current git revision if available (best-effort; the
# agent ignores it today, but it makes the produced binary traceable).
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo unknown)"

echo "Building horstoperator-agent for windows/amd64"
echo "  version : ${VERSION}"
echo "  output  : ${OUT}"

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build \
    -trimpath \
    -ldflags "-s -w" \
    -o "$OUT" \
    ./cmd/horstoperator-agent

echo "Done: ${OUT}"
ls -lh "$OUT"
