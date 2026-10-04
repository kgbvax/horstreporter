#!/bin/bash
# Postgres tuning for horstreporter.kgbvax.net (profile: docs/performance.md).
#
#   ./scripts/pg-tune.sh            apply the no-restart changes and stage the memory drop-in
#   ./scripts/pg-tune.sh --restart  ... and restart postgresql@17-main so shared_buffers takes effect
#
# What it changes
#   - ALTER ROLE dxuser SET work_mem = 32MB          (new sessions; the app pool is 4 connections)
#   - per-table autovacuum thresholds on the hot update-heavy baseline tables
#   - conf.d/20-horst-memory.conf: shared_buffers 128MB -> 2GB (restart), effective_cache_size 4GB -> 5GB
# Nothing is rewritten and no table is locked beyond the instant of ALTER TABLE.
#
# Rollback
#   ssh root@HOST 'rm /etc/postgresql/17/main/conf.d/20-horst-memory.conf && systemctl restart postgresql@17-main'
#   ALTER ROLE dxuser RESET work_mem;
#   ALTER TABLE <t> RESET (autovacuum_vacuum_scale_factor, autovacuum_vacuum_threshold, autovacuum_analyze_scale_factor);
#
# A restart drops the app's DB connections for a few seconds: the pool reconnects, the ingest
# flush queues retry, and requests needing Postgres fall back to in-memory paths meanwhile.
set -euo pipefail

HOST="horstreporter.kgbvax.net"
RESTART=0
[ "${1:-}" = "--restart" ] && RESTART=1

ssh "root@${HOST}" RESTART="$RESTART" 'bash -s' <<'REMOTE'
set -euo pipefail
CONF=/etc/postgresql/17/main
STAMP=$(date -u +%Y%m%d-%H%M%S)

echo "== before"
sudo -u postgres psql -d dxdata -Atc "select name||'='||setting||coalesce(unit,'') from pg_settings where name in ('shared_buffers','effective_cache_size','work_mem')"

echo "== backup conf.d -> /root/pg-conf.d.backup-$STAMP"
cp -a "$CONF/conf.d" "/root/pg-conf.d.backup-$STAMP"

echo "== role and table settings (no restart)"
sudo -u postgres psql -d dxdata -v ON_ERROR_STOP=1 <<'SQL'
ALTER ROLE dxuser SET work_mem = '32MB';
ALTER TABLE dx_baseline_cluster        SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_threshold = 2000, autovacuum_analyze_scale_factor = 0.05);
ALTER TABLE dx_baseline_global         SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_threshold = 1000, autovacuum_analyze_scale_factor = 0.05);
ALTER TABLE dx_region_baseline_daily   SET (autovacuum_vacuum_scale_factor = 0.01, autovacuum_vacuum_threshold = 5000, autovacuum_analyze_scale_factor = 0.02);
ALTER TABLE prop_region_baseline_daily SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_threshold = 1000);
ALTER TABLE wspr_region_baseline_daily SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_threshold = 1000);
SQL

echo "== memory drop-in"
cat > "$CONF/conf.d/20-horst-memory.conf" <<'EOF'
# HorstReporter memory tuning (scripts/pg-tune.sh). Box: 4 vCPU, 7.7 GB RAM, 1 GB swap;
# Go app RSS 1-2.4 GB (GOMEMLIMIT 2400 MiB). Rollback: delete this file, restart postgresql@17-main.

# Was the 128 MB default: the baseline / recent raw-spot index working set did not fit, so hot
# blocks were re-read through the OS cache on every query. (restart required)
shared_buffers       = 2GB

# Planner hint, not an allocation: shared_buffers + the OS cache the app leaves free (~5.7 GB seen).
effective_cache_size = 5GB
EOF
chown postgres:postgres "$CONF/conf.d/20-horst-memory.conf"
chmod 644 "$CONF/conf.d/20-horst-memory.conf"

echo "== validate"
sudo -u postgres /usr/lib/postgresql/17/bin/postgres -C shared_buffers -c config_file="$CONF/postgresql.conf"
sudo -u postgres /usr/lib/postgresql/17/bin/postgres -C effective_cache_size -c config_file="$CONF/postgresql.conf"

if [ "$RESTART" = "1" ]; then
  echo "== restart postgresql@17-main"
  systemctl restart postgresql@17-main
  for i in $(seq 1 30); do pg_isready -q -h /var/run/postgresql && break; sleep 1; done
  pg_isready -h /var/run/postgresql
  echo "== after"
  sudo -u postgres psql -d dxdata -Atc "select name||'='||setting||coalesce(unit,'') from pg_settings where name in ('shared_buffers','effective_cache_size','work_mem')"
  sudo -u postgres psql -d dxdata -Atc "select 'dxuser work_mem: '||coalesce((select array_to_string(rolconfig, ',') from pg_roles where rolname='dxuser'),'-')"
  sleep 15
  echo "== app health"
  curl -s --resolve horstreporter.kgbvax.net:443:127.0.0.1 https://horstreporter.kgbvax.net/api/stats | tr ',' '\n' | grep -E 'raw_flush|baseline_flush|active_connections' || true
  free -m | head -2
else
  echo "== staged: shared_buffers/effective_cache_size apply at the next restart (re-run with --restart)"
fi
REMOTE
