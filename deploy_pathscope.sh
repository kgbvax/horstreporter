#!/bin/bash

# Deploys pathscope to the horstreporter server. Mirrors deploy.sh but
# targets the pathscope binary. Pathscope runs as hk:hk on the same box
# as the main horstreporter binary, sharing /opt/horstreporter.
#
# Usage:
#   ./deploy_pathscope.sh                     # default: root@horstreporter.kgbvax.net
#   ./deploy_pathscope.sh <user>              # explicit SSH user
#   PATHSCOPE_HOST=other.host ./deploy_pathscope.sh

set -euo pipefail

HOST="${PATHSCOPE_HOST:-horstreporter.kgbvax.net}"
USER="${1:-${PATHSCOPE_USER:-root}}"
BINARY_NAME="pathscope-linux-x64"

if [ "$HOST" = "" ]; then
    echo "Usage: PATHSCOPE_HOST=<host> $0  (or: $0 <user> when PATHSCOPE_HOST is set)"
    exit 1
fi

echo "Building $BINARY_NAME..."
./build_pathscope_linux_x64.sh

echo "Uploading $BINARY_NAME, service file, and installer to ${USER}@${HOST}..."
scp "$BINARY_NAME"     "cmd/pathscope/pathscope.service"     "install_pathscope_service.sh"     "${USER}@${HOST}:/tmp/"

echo "Running installer on ${HOST}..."
ssh "${USER}@${HOST}" 'cd /tmp && sudo ./install_pathscope_service.sh && sudo systemctl status pathscope --no-pager'

echo "Deployment complete!"
