#!/usr/bin/env bash
# Generate a Web Push VAPID key pair for the backend's -push-enable.
#
#   scripts/generate-vapid-keys.sh              # print env lines to stdout
#   scripts/generate-vapid-keys.sh FILE         # append them to FILE (0600)
#
# FILE is a dev .env or the production EnvironmentFile
# (/etc/default/horstreporter on prod, see docs/deployment-secrets.md).
# Refuses to touch a FILE that already has VAPID keys: rotating the pair
# invalidates every existing browser subscription.
set -euo pipefail

cd "$(dirname "$0")/.."

keys="$(go run ./scripts/vapidkeys)"

if [[ $# -eq 0 ]]; then
    printf '%s\n' "$keys"
    exit 0
fi

file="$1"
if [[ -f "$file" ]] && grep -q '^PUSH_VAPID_' "$file"; then
    echo "error: $file already has PUSH_VAPID_* keys; remove them first to rotate" >&2
    exit 1
fi
umask 077
if [[ -s "$file" && -n "$(tail -c1 "$file")" ]]; then
    echo >>"$file"
fi
printf '%s\n' "$keys" >>"$file"
chmod 600 "$file"
echo "wrote VAPID keys to $file" >&2
