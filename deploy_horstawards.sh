#!/bin/bash

# Deploys horstawards to your operator/shack box (build locally, upload, install,
# restart). Mirrors deploy_horstprop.sh — horstawards is operator-LOCAL (the agent
# reaches it at 127.0.0.1:9956), so it must run on the machine you operate from,
# NOT the public HorstReporter server.
#
# Usage:
#   HORSTAWARDS_HOST=shack.lan ./deploy_horstawards.sh           # remote (ssh/scp)
#   HORSTAWARDS_HOST=shack.lan HORSTAWARDS_USER=ingo ./deploy_horstawards.sh
#   ./deploy_horstawards.sh <host> [user]
#
# Running ON the shack box itself? Skip this and just:
#   ./build_horstawards_linux_x64.sh && sudo ./install_horstawards_service.sh

set -e

HOST="${HORSTAWARDS_HOST:-${1:-}}"
USER="${HORSTAWARDS_USER:-${2:-root}}"
BINARY_NAME="horstawards-linux-x64"

if [ -z "$HOST" ]; then
    echo "No target host. Set HORSTAWARDS_HOST or pass it as the first argument."
    echo "Usage: HORSTAWARDS_HOST=shack.lan $0   (or: $0 <host> [user])"
    exit 1
fi

echo "Building $BINARY_NAME..."
./build_horstawards_linux_x64.sh

echo "Uploading to ${USER}@${HOST}..."
scp "$BINARY_NAME" install_horstawards_service.sh "${USER}@${HOST}:/tmp/"

echo "Installing and restarting horstawards on ${HOST}..."
ssh "${USER}@${HOST}" 'cd /tmp && sudo ./install_horstawards_service.sh && sudo systemctl status horstawards --no-pager'

echo "Deployment complete!"
