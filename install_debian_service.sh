#!/bin/bash

# Exit on error
set -e

if [ "$EUID" -ne 0 ]; then
  echo "Please run this script as root (e.g., using sudo)"
  exit 1
fi

# Variables
APP_NAME="horstreporter"
APP_USER="hk"
INSTALL_DIR="/opt/$APP_NAME"
BINARY_NAME="horstreporter-linux-x64"

# 1. Create the user 'hk' if it doesn't already exist
if ! id "$APP_USER" &>/dev/null; then
    echo "Creating user $APP_USER..."
    useradd -r -s /usr/sbin/nologin "$APP_USER"
fi

# 2. Setup directories
echo "Setting up installation directories at $INSTALL_DIR..."
mkdir -p "$INSTALL_DIR"
mkdir -p "$INSTALL_DIR/certs"
mkdir -p "/var/log/$APP_NAME"

# 3. Check and copy the binary
if [ ! -f "$BINARY_NAME" ]; then
    echo "Error: Binary '$BINARY_NAME' not found in the current directory."
    echo "Please build it first using ./build_linux_x64.sh static"
    exit 1
fi

echo "Copying binary..."
cp "$BINARY_NAME" "$INSTALL_DIR/"
chmod +x "$INSTALL_DIR/$BINARY_NAME"

# Set correct ownership for the service user
chown -R "$APP_USER:$APP_USER" "$INSTALL_DIR"
chown -R "$APP_USER:$APP_USER" "/var/log/$APP_NAME"

# 3b. cty.dat for DX-cluster country/flag labelling. Fetched here, refreshed
# weekly by a systemd timer (see below). horstreporter loads it via CTY_DAT_PATH.
CTY_PATH="$INSTALL_DIR/cty.dat"
CTY_URL="${CTY_URL:-https://www.country-files.com/bigcty/cty.dat}"
echo "Fetching cty.dat from $CTY_URL..."
if curl -fsSL --max-time 60 "$CTY_URL" -o "$INSTALL_DIR/cty.dat.tmp" && grep -q ':.*:.*:.*:' "$INSTALL_DIR/cty.dat.tmp"; then
    mv "$INSTALL_DIR/cty.dat.tmp" "$CTY_PATH"
    chown "$APP_USER:$APP_USER" "$CTY_PATH"
    echo "cty.dat installed at $CTY_PATH ($(wc -c < "$CTY_PATH") bytes)"
else
    rm -f "$INSTALL_DIR/cty.dat.tmp"
    echo "WARNING: cty.dat download failed — country/flag labelling stays off until $CTY_PATH exists"
fi

# 4. Create an environment configuration file for easy arg modifications
DEFAULT_CONFIG="/etc/default/$APP_NAME"
if [ ! -f "$DEFAULT_CONFIG" ]; then
    echo "Creating default configuration file at $DEFAULT_CONFIG..."
    cat <<EOF > "$DEFAULT_CONFIG"
# horstreporter command line arguments
# To enable Let's Encrypt (which will utilize the allowed 80/443 ports), provide your domain:
#ARGS="-port 443 -domain example.com -pprof"
# To enable compression of the SSE stream to save bandwidth:
# ARGS="-port 80 -compress"
# To enable internal pprof profiling on localhost:6060:
# ARGS="-port 80 -compress -pprof"
# To enable file logging (rotated and gzipped automatically):
ARGS="-port 80 -port 443 -domain example.com -pprof -log-file /var/log/$APP_NAME/$APP_NAME.log -log-max-age 14 -log-max-size 50"
# To use PostgreSQL/PostGIS for baseline+raw spots:
# ARGS="-port 80 -domain example.com -dx-postgres-dsn postgres://dxuser:YOUR_PASSWORD@localhost:5432/dxdata?sslmode=disable"
# Path to cty.dat for DX-cluster country/flag labelling:
CTY_DAT_PATH="$CTY_PATH"
EOF
fi

# Ensure CTY_DAT_PATH is present even for pre-existing config files.
if ! grep -q '^CTY_DAT_PATH=' "$DEFAULT_CONFIG"; then
    echo "CTY_DAT_PATH=\"$CTY_PATH\"" >> "$DEFAULT_CONFIG"
fi

# 5. Create Systemd Service
SERVICE_FILE="/etc/systemd/system/$APP_NAME.service"
echo "Creating systemd service at $SERVICE_FILE..."

cat <<EOF > "$SERVICE_FILE"
[Unit]
Description=HorstReporter Service
After=network.target

[Service]
Type=simple
User=$APP_USER
Group=$APP_USER
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=-$DEFAULT_CONFIG
ExecStart=$INSTALL_DIR/$BINARY_NAME \$ARGS
Restart=always
RestartSec=5

# Allow unprivileged user to bind to ports < 1024 (e.g., 80 and 443)
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

# 5b. Weekly cty.dat refresh: an updater script + a systemd timer. cty.dat is
# read at startup, so the updater restarts the service after a successful fetch
# (brief; SSE clients auto-reconnect). It keeps the old file if the fetch fails.
UPDATER="/usr/local/bin/$APP_NAME-update-cty"
echo "Installing cty.dat refresh updater + timer..."
cat <<EOF > "$UPDATER"
#!/bin/bash
set -euo pipefail
DEST="$CTY_PATH"
URL="\${CTY_URL:-$CTY_URL}"
TMP="\$(mktemp)"
if curl -fsSL --max-time 60 "\$URL" -o "\$TMP" && grep -q ':.*:.*:.*:' "\$TMP"; then
    mv "\$TMP" "\$DEST"
    chown $APP_USER:$APP_USER "\$DEST" || true
    systemctl try-restart $APP_NAME
    echo "cty.dat refreshed; $APP_NAME restarted"
else
    rm -f "\$TMP"
    echo "cty.dat refresh failed; keeping existing file" >&2
    exit 1
fi
EOF
chmod +x "$UPDATER"

cat <<EOF > "/etc/systemd/system/$APP_NAME-cty.service"
[Unit]
Description=Refresh cty.dat for $APP_NAME
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=$UPDATER
EOF

cat <<EOF > "/etc/systemd/system/$APP_NAME-cty.timer"
[Unit]
Description=Weekly cty.dat refresh for $APP_NAME

[Timer]
OnCalendar=weekly
Persistent=true
RandomizedDelaySec=1h

[Install]
WantedBy=timers.target
EOF

# 6. Enable and start the service + the cty refresh timer
echo "Reloading systemd, enabling and starting $APP_NAME..."
systemctl daemon-reload
systemctl enable $APP_NAME
systemctl restart $APP_NAME
systemctl enable --now "$APP_NAME-cty.timer"

echo "Installation complete!"
echo "Check the service status using: systemctl status $APP_NAME"
echo "You can configure ports and domains in: $DEFAULT_CONFIG"
echo "cty.dat: $CTY_PATH (refreshed weekly; manual: $UPDATER)"