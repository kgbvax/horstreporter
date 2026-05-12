#!/bin/bash

# Exit on error
set -e

HOST="horstreporter.kgbvax.net"
# Default to root, but allow overriding by passing a username as the first argument
USER=${1:-root}

echo "Building Linux x64 binary..."
./build_linux_x64.sh

echo "Uploading binary to ${USER}@${HOST}..."
scp horstreporter-linux-x64 "${USER}@${HOST}:/tmp/"

echo "Installing and restarting service on ${HOST}..."
ssh "${USER}@${HOST}" << 'EOF'
    sudo systemctl stop horstreporter
    sudo mv /tmp/horstreporter-linux-x64 /opt/horstreporter/
    sudo chmod +x /opt/horstreporter/horstreporter-linux-x64
    sudo chown hk:hk /opt/horstreporter/horstreporter-linux-x64
    sudo systemctl start horstreporter
    echo "Service status:"
    sudo systemctl status horstreporter --no-pager
EOF

echo "Deployment complete!"