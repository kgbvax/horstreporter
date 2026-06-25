#!/bin/bash

# Deploys horstprop to your operator/shack box (build locally, upload, install,
# restart). Mirrors deploy.sh, but the target is configurable because horstprop
# is operator-LOCAL — the browser reaches it at 127.0.0.1:9970, so it must run on
# the machine you operate from, NOT the public HorstReporter server.
#
# Usage:
#   HORSTPROP_HOST=shack.lan ./deploy_horstprop.sh           # remote (ssh/scp)
#   HORSTPROP_HOST=shack.lan HORSTPROP_USER=ingo ./deploy_horstprop.sh
#   ./deploy_horstprop.sh <host> [user]
#
# Running ON the shack box itself? Skip this and just:
#   ./build_horstprop_linux_x64.sh && sudo ./install_horstprop_service.sh

set -e

HOST="${HORSTPROP_HOST:-${1:-}}"
USER="${HORSTPROP_USER:-${2:-root}}"
BINARY_NAME="horstprop-linux-x64"

if [ -z "$HOST" ]; then
    echo "No target host. Set HORSTPROP_HOST or pass it as the first argument."
    echo "Usage: HORSTPROP_HOST=shack.lan $0   (or: $0 <host> [user])"
    exit 1
fi

echo "Building $BINARY_NAME..."
./build_horstprop_linux_x64.sh

echo "Uploading to ${USER}@${HOST}..."
scp "$BINARY_NAME" install_horstprop_service.sh "${USER}@${HOST}:/tmp/"

echo "Installing and restarting horstprop on ${HOST}..."
ssh "${USER}@${HOST}" 'cd /tmp && sudo ./install_horstprop_service.sh && sudo systemctl status horstprop --no-pager'

echo "Deployment complete!"
