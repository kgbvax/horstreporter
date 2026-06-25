#!/bin/bash

# Installs horstprop as a systemd service. Run as root ON THE TARGET machine
# (your operator/shack box). Idempotent: re-run to update the binary; it won't
# clobber an existing /etc/default/horstprop.
#
# Mirrors install_debian_service.sh. horstprop holds NO secrets (KC2G is public)
# so the config is just command-line args.

set -e

if [ "$EUID" -ne 0 ]; then
  echo "Please run this script as root (e.g., using sudo)"
  exit 1
fi

APP_NAME="horstprop"
APP_USER="hk"
INSTALL_DIR="/opt/$APP_NAME"
BINARY_NAME="horstprop-linux-x64"

# 1. Service user
if ! id "$APP_USER" &>/dev/null; then
    echo "Creating user $APP_USER..."
    useradd -r -s /usr/sbin/nologin "$APP_USER"
fi

# 2. Directory + binary
mkdir -p "$INSTALL_DIR"
if [ ! -f "$BINARY_NAME" ]; then
    echo "Error: '$BINARY_NAME' not found in $(pwd)."
    echo "Build it first: ./build_horstprop_linux_x64.sh"
    exit 1
fi
echo "Installing binary to $INSTALL_DIR..."
# Atomic replace: rename works even when the current binary is running (a plain
# cp over it fails with ETXTBSY). The running process keeps the old inode until
# the restart below swaps to the new file.
cp "$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME.new"
chmod +x "$INSTALL_DIR/$BINARY_NAME.new"
mv -f "$INSTALL_DIR/$BINARY_NAME.new" "$INSTALL_DIR/$BINARY_NAME"
chown -R "$APP_USER:$APP_USER" "$INSTALL_DIR"

# 3. Config (args only). Edit to point at your HorstReporter and set your grid.
DEFAULT_CONFIG="/etc/default/$APP_NAME"
if [ ! -f "$DEFAULT_CONFIG" ]; then
    echo "Creating default configuration at $DEFAULT_CONFIG..."
    cat <<EOF > "$DEFAULT_CONFIG"
# horstprop command-line arguments.
#   -listen     : where the browser/Chase Queue reaches it (keep 127.0.0.1 if the
#                 browser runs on this same machine; use a LAN IP otherwise).
#   -home-grid  : your station Maidenhead locator.
#   -horst-url  : the HorstReporter base URL it consumes read-only
#                 (e.g. https://horstreporter.kgbvax.net or http://127.0.0.1:80).
#   -area-rings : area-of-interest radius in grid-square rings around home.
#   -cty-path   : optional AD1C cty.dat for callsign->DXCC centroid.
ARGS="-listen 127.0.0.1:9970 -home-grid JO32we -horst-url https://horstreporter.kgbvax.net -area-rings 6"
EOF
fi

# 4. systemd unit
SERVICE_FILE="/etc/systemd/system/$APP_NAME.service"
echo "Writing systemd unit at $SERVICE_FILE..."
cat <<EOF > "$SERVICE_FILE"
[Unit]
Description=horstprop HF link-quality scoring service
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
# hardening (no privileged port, no secrets, no writes outside its dir)
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=$INSTALL_DIR

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
echo "Config: $DEFAULT_CONFIG  (edit -home-grid / -horst-url, then: systemctl restart $APP_NAME)"
