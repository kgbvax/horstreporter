#!/bin/bash

# Exit on error
set -e

HOST="horstreporter.kgbvax.net"
# Default to root, but allow overriding by passing a username as the first argument
USER=${1:-root}

# Build mode: static only
MODE=${2:-static}
BINARY_NAME="horstreporter-linux-x64"
if [ "$MODE" != "static" ]; then
    echo "Usage: $0 [user] [static]"
    exit 1
fi

echo "Building Linux x64 binary (mode=$MODE)..."
./build_linux_x64.sh "$MODE"

echo "Uploading $BINARY_NAME to ${USER}@${HOST}..."
scp "$BINARY_NAME" "${USER}@${HOST}:/tmp/"

echo "Installing and restarting service on ${HOST}..."
ssh "${USER}@${HOST}" << 'EOF'
    sudo systemctl stop horstreporter
    if [ -f /tmp/horstreporter-linux-x64 ]; then
        sudo mv /tmp/horstreporter-linux-x64 /opt/horstreporter/
        sudo chmod +x /opt/horstreporter/horstreporter-linux-x64
        sudo chown hk:hk /opt/horstreporter/horstreporter-linux-x64
    fi
    sudo systemctl start horstreporter
    echo "Service status:"
    sudo systemctl status horstreporter --no-pager
EOF

echo "Deployment complete!"
