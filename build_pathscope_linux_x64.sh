#!/bin/bash

# Builds the standalone pathscope HTTP server for Linux x64. Pathscope
# has no UI bundle to compile (it embeds static/ via //go:embed and the
# static dir is plain CSS/HTML/JS, no Vite step), so this is just the
# Go build.

set -euo pipefail

BIN="pathscope-linux-x64"

echo "Building statically linked pathscope for Linux x64 (CGO disabled)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o "$BIN" ./cmd/pathscope
echo "Build complete: $BIN"