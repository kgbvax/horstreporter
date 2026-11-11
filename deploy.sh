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

    # Flags that no longer exist: Go's flag parser exits on unknown flags and
    # would crash-loop the service. Strip them from ARGS in the EnvironmentFile
    # (idempotent; keeps a .bak of the file when something changed).
    # REMOVED_FLAGS take a value ("-f v" or "-f=v"): the next token goes too.
    # REMOVED_BOOL_FLAGS are booleans ("-f" or "-f=true"): never swallow the
    # next token, which is the following flag.
    REMOVED_FLAGS="horstprop-url opmode-agent-url pathscope-url proplab-cell-retention-days dxcluster-endpoint dxcluster-reconnect-seconds dxcluster-username dxcluster-password"
    REMOVED_BOOL_FLAGS="proplab-sw-enable dxcluster-enable dxcluster-verbose"
    ENVFILE=/etc/default/horstreporter
    if [ -f "$ENVFILE" ]; then
        BACKED_UP=0
        backup_once() {
            if [ "$BACKED_UP" = 0 ]; then
                sudo cp -p "$ENVFILE" "${ENVFILE}.bak"
                BACKED_UP=1
            fi
        }
        for f in $REMOVED_FLAGS; do
            if sudo grep -Eq -- "-{1,2}${f}([= ][^ \"']*)?" "$ENVFILE"; then
                echo "Removing obsolete flag -${f} from ${ENVFILE}"
                backup_once
                sudo sed -E -i "s/ ?-{1,2}${f}([= ][^ \"']*)?//g" "$ENVFILE"
            fi
        done
        for f in $REMOVED_BOOL_FLAGS; do
            if sudo grep -Eq -- "-{1,2}${f}(=[^ \"']*)?( |\"|$)" "$ENVFILE"; then
                echo "Removing obsolete flag -${f} from ${ENVFILE}"
                backup_once
                sudo sed -E -i "s/ ?-{1,2}${f}(=[^ \"']*)?( |\"|$)/\2/g" "$ENVFILE"
            fi
        done
    fi
    if [ -f /tmp/horstreporter-linux-x64 ]; then
        sudo mv /tmp/horstreporter-linux-x64 /opt/horstreporter/
        sudo chmod +x /opt/horstreporter/horstreporter-linux-x64
        sudo chown hk:hk /opt/horstreporter/horstreporter-linux-x64
    fi
    sudo systemctl start horstreporter

    # horstvideo sidecar retired with the time-travel feature — tear it down.
    if systemctl list-unit-files horstvideo.service >/dev/null 2>&1; then
        sudo systemctl disable --now horstvideo || true
        sudo rm -f /etc/systemd/system/horstvideo.service /opt/horstreporter/horstvideo-linux-x64
        sudo systemctl daemon-reload
    fi

    echo "Service status:"
    sudo systemctl status horstreporter --no-pager
EOF

echo "Deployment complete!"
