#!/bin/bash

set -euo pipefail

# Builds the standalone horstprop scoring service for Linux x64.
# horstprop is pure Go (no cgo), so a static build is straightforward.

BIN="horstprop-linux-x64"

echo "Building statically linked horstprop for Linux x64 (CGO disabled)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o "$BIN" ./cmd/horstprop
echo "Build complete: $BIN"
