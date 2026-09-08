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
# Atomic replace (rename) so re-running while the service is live doesn't hit
# ETXTBSY ("Text file busy") on the running executable.
cp "$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME.new"
chmod +x "$INSTALL_DIR/$BINARY_NAME.new"
mv -f "$INSTALL_DIR/$BINARY_NAME.new" "$INSTALL_DIR/$BINARY_NAME"

# Set correct ownership for the service user
chown -R "$APP_USER:$APP_USER" "$INSTALL_DIR"
chown -R "$APP_USER:$APP_USER" "/var/log/$APP_NAME"

# Note: cty.dat (DX-cluster country/flag labelling) is embedded in the binary —
# no file to deploy. Override with -cty-path only if you want a fresher cty.dat.

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
EOF
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

# 6. Install nightly Postgres vacuum/analyze cron job.
#    Hot tables churn quickly; autovacuum handles most of it, but a
#    daily explicit VACUUM ANALYZE is a cheap safety net against bloat.
CRON_FILE="/etc/cron.d/horstreporter-vacuum"
echo "Installing nightly vacuum cron to $CRON_FILE..."
cat <<'EOF' > "$CRON_FILE"
# Nightly catch-up vacuum/analyze for HorstReporter hot tables.
# Runs as postgres; low-traffic time (03:43 UTC).  Uses plain VACUUM ANALYZE
# (not VACUUM FULL) so it does not block concurrent ingest/queries.
43 3 * * * postgres /usr/bin/psql -d dxdata -c "VACUUM (ANALYZE) dx_raw_spots, dx_region_baseline_daily, proplab_cell_buckets, dx_baseline_global, proplab_sw_series;"
EOF
chmod 0644 "$CRON_FILE"

# 7. Enable and start the service
echo "Reloading systemd, enabling and starting $APP_NAME..."
systemctl daemon-reload
systemctl enable $APP_NAME
systemctl restart $APP_NAME

echo "Installation complete!"
echo "Check the service status using: systemctl status $APP_NAME"
echo "You can configure ports and domains in: $DEFAULT_CONFIG"
echo "Postgres maintenance cron installed in: $CRON_FILE"