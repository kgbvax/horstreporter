#!/bin/bash

set -euo pipefail

# Builds the standalone horstawards award-progress service for Linux x64.
# horstawards is pure Go (no cgo), so a static build is straightforward.

BIN="horstawards-linux-x64"

echo "Building statically linked horstawards for Linux x64 (CGO disabled)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o "$BIN" ./cmd/horstawards
echo "Build complete: $BIN"
