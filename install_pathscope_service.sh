#!/bin/bash

# Installs pathscope as a systemd service. Run as root ON THE TARGET machine.
# Idempotent: re-run to update the binary; it won't clobber an existing
# /etc/default/pathscope or /etc/horstreporter/pathscope.env.
#
# Mirrors install_horstprop_service.sh. Pathscope DOES hold a secret (the
# Postgres DSN), so the secret lives in /etc/horstreporter/pathscope.env
# rather than /etc/default/pathscope. The systemd unit points at both:
#   - /etc/default/pathscope             -> command-line args (non-secret)
#   - /etc/horstreporter/pathscope.env   -> DX_POSTGRES_DSN (the secret)

set -e

if [ "$EUID" -ne 0 ]; then
  echo "Please run this script as root (e.g., using sudo)"
  exit 1
fi

APP_NAME="pathscope"
APP_USER="hk"
APP_GROUP="hk"
INSTALL_DIR="/opt/horstreporter"
BINARY_NAME="pathscope-linux-x64"
SCHEMA_FILE="schema/pathscope.sql"

# 1. Service user (idempotent: skip if present)
if ! id "$APP_USER" &>/dev/null; then
    echo "Creating user $APP_USER..."
    useradd -r -s /usr/sbin/nologin "$APP_USER"
fi

# 2. Directory + binary (atomic replace so the running process keeps its
#    inode until systemd restarts it).
mkdir -p "$INSTALL_DIR"
if [ ! -f "$BINARY_NAME" ]; then
    echo "Error: '$BINARY_NAME' not found in $(pwd)."
    echo "Build it first on the dev box: ./build_pathscope_linux_x64.sh"
    exit 1
fi
echo "Installing binary to $INSTALL_DIR..."
cp "$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME.new"
chmod +x "$INSTALL_DIR/$BINARY_NAME.new"
mv -f "$INSTALL_DIR/$BINARY_NAME.new" "$INSTALL_DIR/$BINARY_NAME"
# Pathscope shares /opt/horstreporter with the main binary. chown only
# the new file so we don't disturb the rest of the directory; the main
# horstreporter binary is already owned by hk:hk.
chown "$APP_USER:$APP_GROUP" "$INSTALL_DIR/$BINARY_NAME"

# 3. Non-secret defaults. These are visible in `systemctl show` so they
#    contain only listening address + QTH.
DEFAULT_CONFIG="/etc/default/$APP_NAME"
if [ ! -f "$DEFAULT_CONFIG" ]; then
    echo "Creating default configuration at $DEFAULT_CONFIG..."
    cat <<EOF > "$DEFAULT_CONFIG"
# pathscope command-line arguments (non-secret).
#   -listen : where nginx (or curl) reaches it. Keep 127.0.0.1 when behind
#             a reverse proxy on the same host.
#   -qth    : your station Maidenhead locator (4 or 6 chars).
ARGS="-listen 127.0.0.1:9960 -qth JO62qm"
EOF
fi

# 4. Secret env file. /etc/horstreporter/pathscope.env is the same
#    EnvironmentFile convention the main horstreporter.service uses; it
#    is mode 0600 owned by hk:hk.
SECRETS_DIR="/etc/horstreporter"
SECRETS_FILE="$SECRETS_DIR/$APP_NAME.env"
if [ -f "$SECRETS_FILE" ]; then
    echo "Secret env file present at $SECRETS_FILE (leaving untouched)."
else
    mkdir -p "$SECRETS_DIR"
    echo "Creating $SECRETS_FILE - fill in DX_POSTGRES_DSN before starting."
    cat <<EOF > "$SECRETS_FILE"
# Pathscope secrets. Mode 0600, owned by the service user.
# The DSN can use any of:
#   PATHSCOPE_DSN=...    (pathscope-specific)
#   DX_POSTGRES_DSN=...  (shared with the main horstreporter binary;
#                         what the binary reads when PATHSCOPE_DSN is unset)
DX_POSTGRES_DSN=postgres://dxuser:CHANGE_ME@localhost:5432/dxdata?sslmode=disable
EOF
    chmod 0600 "$SECRETS_FILE"
    chown "$APP_USER:$APP_GROUP" "$SECRETS_FILE"
    echo "*** Edit $SECRETS_FILE and set DX_POSTGRES_DSN before starting. ***"
fi

# 5. SQL schema (idempotent CREATE TABLE IF NOT EXISTS). We resolve the
#    schema path against the script's own directory so the installer works
#    no matter what the caller's CWD is (the typical case: the deploy
#    script scp's files into /tmp/ on the host and runs the installer from
#    there, where a relative `schema/pathscope.sql` silently doesn't
#    resolve). We also try a few likely locations before giving up.
SCHEMA_SOURCE=""
for candidate in \
    "$SCHEMA_FILE" \
    "$(dirname "$(readlink -f "$0")")/schema/pathscope.sql" \
    "$INSTALL_DIR/schema/pathscope.sql"; do
    if [ -f "$candidate" ]; then
        SCHEMA_SOURCE="$candidate"
        break
    fi
done
SCHEMA_TARGET="$INSTALL_DIR/schema/pathscope.sql"
if [ -n "$SCHEMA_SOURCE" ]; then
    mkdir -p "$INSTALL_DIR/schema"
    cp "$SCHEMA_SOURCE" "$SCHEMA_TARGET"
    chmod 0644 "$SCHEMA_TARGET"
    chown "$APP_USER:$APP_GROUP" "$SCHEMA_TARGET"
    if grep -q '^DX_POSTGRES_DSN=' "$SECRETS_FILE" 2>/dev/null; then
        DSN_VAL=$(grep '^DX_POSTGRES_DSN=' "$SECRETS_FILE" | head -1 | cut -d= -f2-)
        if [ "$DSN_VAL" != "postgres://dxuser:CHANGE_ME@localhost:5432/dxdata?sslmode=disable" ]; then
            echo "Applying $SCHEMA_FILE to the database..."
            if command -v psql >/dev/null 2>&1; then
                sudo -u "$APP_USER" -E psql "$DSN_VAL" -f "$SCHEMA_TARGET" || \
                    echo "Warning: schema apply failed; run manually: psql \"\$DX_POSTGRES_DSN\" -f $SCHEMA_TARGET"
            else
                echo "psql not found on this host. Apply $SCHEMA_TARGET manually:"
                echo "    psql \"\$DX_POSTGRES_DSN\" -f $SCHEMA_TARGET"
            fi
        else
            echo "DX_POSTGRES_DSN is the install default; skipping schema apply."
        fi
    fi
else
    echo "Note: schema/pathscope.sql not found in $(pwd),"
    echo "      next to the installer, or under $INSTALL_DIR/schema/; skipping schema staging."
    echo "      Apply schema/pathscope.sql manually once the binary is running."
fi

# 6. systemd unit. Renders on every run so the deployed copy tracks the
#    one shipped at cmd/pathscope/pathscope.service.
SERVICE_FILE="/etc/systemd/system/$APP_NAME.service"
SERVICE_SOURCE="cmd/pathscope/$APP_NAME.service"
if [ -f "$SERVICE_SOURCE" ]; then
    echo "Installing systemd unit from $SERVICE_SOURCE..."
    cp "$SERVICE_SOURCE" "$SERVICE_FILE"
else
    echo "Writing fallback systemd unit at $SERVICE_FILE..."
    cat <<EOF > "$SERVICE_FILE"
[Unit]
Description=horstreporter pathscope - band x region opening forecast
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$APP_USER
Group=$APP_GROUP
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=-$SECRETS_FILE
EnvironmentFile=-$DEFAULT_CONFIG
ExecStart=$INSTALL_DIR/$BINARY_NAME
Restart=on-failure
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=$INSTALL_DIR

[Install]
WantedBy=multi-user.target
EOF
fi

# 7. Enable + (re)start
echo "Reloading systemd, enabling and starting $APP_NAME..."
systemctl daemon-reload
systemctl enable "$APP_NAME"
systemctl restart "$APP_NAME"

echo
echo "Installation complete!"
echo "Status:  systemctl status $APP_NAME --no-pager"
echo "Logs:    journalctl -u $APP_NAME -f"
echo "Config:  $DEFAULT_CONFIG       (non-secret args)"
echo "Secrets: $SECRETS_FILE         (DB DSN; chmod 0600)"
if [ -f "$SERVICE_FILE" ]; then
    echo "Unit:    $SERVICE_FILE"
fi
