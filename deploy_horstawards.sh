#!/bin/bash

# Deploys horstawards to the HorstReporter server (build locally, upload, install,
# restart). horstawards runs CO-LOCATED with the backend on kgbvax.net, bound to
# 127.0.0.1:9956; the backend reverse-proxies /horstawards/ to it, and the
# operator's (local) agent reaches it via that proxy. This mirrors deploy.sh — the
# default host is the public HorstReporter server, NOT a separate shack box.
#
# Usage:
#   ./deploy_horstawards.sh                       # deploy to horstreporter.kgbvax.net (root)
#   ./deploy_horstawards.sh <user>                # override ssh user
#   HORSTAWARDS_HOST=other.host ./deploy_horstawards.sh   # override host
#
# Secrets/config (WAVELOG_API_KEY, WAVELOG_STATION_ID, -pota-hunted-csv path) live
# in /etc/default/horstawards on the server (created chmod 600 by the installer on
# first run; edit it there, then: systemctl restart horstawards).

set -e

HOST="${HORSTAWARDS_HOST:-horstreporter.kgbvax.net}"
USER="${1:-root}"
BINARY_NAME="horstawards-linux-x64"

echo "Building $BINARY_NAME..."
./build_horstawards_linux_x64.sh

echo "Uploading to ${USER}@${HOST}..."
scp "$BINARY_NAME" install_horstawards_service.sh "${USER}@${HOST}:/tmp/"

echo "Installing and restarting horstawards on ${HOST}..."
ssh "${USER}@${HOST}" 'cd /tmp && sudo ./install_horstawards_service.sh && sudo systemctl status horstawards --no-pager'

echo "Deployment complete!"
