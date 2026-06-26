#!/bin/bash

# Installs horstawards as a systemd service. Run as root ON THE TARGET machine
# (your operator/shack box). Idempotent: re-run to update the binary; it won't
# clobber an existing /etc/default/horstawards (which holds your secrets).
#
# Mirrors install_horstprop_service.sh, but horstawards DOES hold a secret (the
# read-only WAVELOG_API_KEY) and needs a writable data dir for its snapshot store,
# so the EnvironmentFile is created chmod 600 and a data dir is provisioned.

set -e

if [ "$EUID" -ne 0 ]; then
  echo "Please run this script as root (e.g., using sudo)"
  exit 1
fi

APP_NAME="horstawards"
APP_USER="hk"
INSTALL_DIR="/opt/$APP_NAME"
DATA_DIR="$INSTALL_DIR/data"
BINARY_NAME="horstawards-linux-x64"

# 1. Service user
if ! id "$APP_USER" &>/dev/null; then
    echo "Creating user $APP_USER..."
    useradd -r -s /usr/sbin/nologin "$APP_USER"
fi

# 2. Directory + binary + data dir
mkdir -p "$INSTALL_DIR" "$DATA_DIR"
if [ ! -f "$BINARY_NAME" ]; then
    echo "Error: '$BINARY_NAME' not found in $(pwd)."
    echo "Build it first: ./build_horstawards_linux_x64.sh"
    exit 1
fi
echo "Installing binary to $INSTALL_DIR..."
# Atomic replace: rename works even when the current binary is running (a plain
# cp over it fails with ETXTBSY).
cp "$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME.new"
chmod +x "$INSTALL_DIR/$BINARY_NAME.new"
mv -f "$INSTALL_DIR/$BINARY_NAME.new" "$INSTALL_DIR/$BINARY_NAME"
chown -R "$APP_USER:$APP_USER" "$INSTALL_DIR"

# 3. Config + secrets (EnvironmentFile). Created once, chmod 600, never clobbered.
DEFAULT_CONFIG="/etc/default/$APP_NAME"
if [ ! -f "$DEFAULT_CONFIG" ]; then
    echo "Creating default configuration at $DEFAULT_CONFIG (edit to add your secrets)..."
    cat <<EOF > "$DEFAULT_CONFIG"
# horstawards configuration + secrets. Keep this file chmod 600.
#
# Secrets (env only — never command-line args):
#   WAVELOG_API_KEY : read-only Wavelog API key (same key the operator agent uses).
WAVELOG_API_KEY=
#   WAVELOG_URL     : Wavelog base URL (defaults to DCLNext if unset).
#WAVELOG_URL=https://log.dclnext.darc.de/index.php
#   WAVELOG_STATION_ID : station profile id. REQUIRED for DCLNext —
#                        get_contacts_adif returns HTTP 400 without it.
WAVELOG_STATION_ID=
#
# POTA (optional). Recommended: export your hunted-parks CSV from POTA and point
# -pota-hunted-csv at it (see ARGS). The POTA API source below is only useful with
# an authenticated full-list endpoint (the public profile is counts-only).
#POTA_CALLSIGN=
#POTA_TOKEN=
#
# Non-secret args:
#   -listen          : where the operator agent reaches it (keep 127.0.0.1).
#   -data-dir        : snapshot store location.
#   -pota-hunted-csv : path to your POTA hunted-parks CSV export (enables POTA).
ARGS="-listen 127.0.0.1:9956 -data-dir $DATA_DIR"
EOF
    chmod 600 "$DEFAULT_CONFIG"
    chown "$APP_USER:$APP_USER" "$DEFAULT_CONFIG"
fi

# 4. systemd unit
SERVICE_FILE="/etc/systemd/system/$APP_NAME.service"
echo "Writing systemd unit at $SERVICE_FILE..."
cat <<EOF > "$SERVICE_FILE"
[Unit]
Description=horstawards award-progress service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$APP_USER
Group=$APP_USER
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=-$DEFAULT_CONFIG
ExecStart=$INSTALL_DIR/$BINARY_NAME \$ARGS
Restart=on-failure
RestartSec=5
# hardening (no privileged port; secrets via EnvironmentFile; writes only to data dir)
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=$DATA_DIR

[Install]
WantedBy=multi-user.target
EOF

# 5. Enable + (re)start
echo "Reloading systemd, enabling and starting $APP_NAME..."
systemctl daemon-reload
systemctl enable "$APP_NAME"
systemctl restart "$APP_NAME"

echo "Installation complete!"
echo "Status: systemctl status $APP_NAME --no-pager"
echo "Config: $DEFAULT_CONFIG  (set WAVELOG_API_KEY, then: systemctl restart $APP_NAME)"
