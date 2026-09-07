#!/bin/bash

set -euo pipefail

MODE="${1:-static}"

STATIC_BIN="horstreporter-linux-x64"

# Build the Svelte UI bundle into static/dist before the Go build embeds
# static/. dist/ is gitignored, so this must run on every fresh checkout.
echo "Building frontend bundle (vite)..."
npm ci
npm run build

case "$MODE" in
	static)
		echo "Building statically linked binary for Linux x64 (CGO disabled)..."
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o "$STATIC_BIN" .
		echo "Build complete: $STATIC_BIN"
		# Video renderer sidecar (cmd/horstvideo) — ships alongside the core so
		# ./deploy.sh can install both in one pass.
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o horstvideo-linux-x64 ./cmd/horstvideo
		echo "Build complete: horstvideo-linux-x64"
		;;
	*)
		echo "Usage: $0 [static]"
		exit 1
		;;
esac