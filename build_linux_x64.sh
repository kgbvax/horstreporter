#!/bin/bash

set -euo pipefail

MODE="${1:-static}"

STATIC_BIN="horstreporter-linux-x64"

case "$MODE" in
	static)
		echo "Building statically linked binary for Linux x64 (CGO disabled)..."
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o "$STATIC_BIN" .
		echo "Build complete: $STATIC_BIN"
		;;
	*)
		echo "Usage: $0 [static]"
		exit 1
		;;
esac