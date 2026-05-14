#!/usr/bin/env bash
# Weekly Postgres base backup: pg_basebackup -> tar -> age encrypt -> mc cp.
# Cron entry (Sun 02:00 EAT, in UTC):
#   0 23 * * 6 /opt/contactcentre/dr/pg_basebackup.sh
#
# Operations:
#   1. pg_basebackup into a fresh staging directory.
#   2. tar+xz the staging dir.
#   3. age encrypt with the recipient pubkey from dr.env.
#   4. mc cp to the off-site bucket under base/YYYYMMDD/.
#   5. Verify the off-site object size matches the local artifact.
#   6. Drop staging.
#   7. Prune off-site objects older than PG_BASE_KEEP_DAYS.

set -euo pipefail

ENV_FILE="${ENV_FILE:-/etc/contactcentre/dr.env}"
# shellcheck disable=SC1090
. "$ENV_FILE"

stamp=$(date -u +%Y%m%dT%H%M%SZ)
date_dir=$(date -u +%Y%m%d)
work="${STAGING_DIR}/base-${stamp}"
artifact="${STAGING_DIR}/base-${stamp}.tar.xz.age"

log() { printf '%s pg_basebackup: %s\n' "$(date -u +%FT%TZ)" "$*"; }

trap 'rm -rf -- "$work"' EXIT
mkdir -p "$work"

log "pg_basebackup -> $work"
pg_basebackup -h "$PGHOST" -U "$PGUSER" -D "$work" --format=tar --gzip \
  --checkpoint=fast --progress --wal-method=stream

log "encrypting -> $artifact"
tar -C "$STAGING_DIR" -cf - "$(basename "$work")" \
  | xz -T0 -3 \
  | age -r "$AGE_RECIPIENT" -o "$artifact"

size=$(stat -c%s "$artifact")
log "artifact size: $size bytes"

key="base/${date_dir}/base-${stamp}.tar.xz.age"
log "uploading to ${MC_ALIAS}/${OFFSITE_BUCKET}/${key}"
mc alias set "$MC_ALIAS" "$MC_ALIAS_URL" "$MC_ACCESS_KEY" "$MC_SECRET_KEY" >/dev/null
mc cp --quiet "$artifact" "${MC_ALIAS}/${OFFSITE_BUCKET#s3://}/${key}"

remote_size=$(mc stat --json "${MC_ALIAS}/${OFFSITE_BUCKET#s3://}/${key}" \
  | sed -n 's/.*"size":\([0-9]*\).*/\1/p')
if [[ "$remote_size" != "$size" ]]; then
  log "size mismatch: local=$size remote=$remote_size"
  exit 2
fi

# Prune base backups older than retention.
cutoff=$(date -u -d "${PG_BASE_KEEP_DAYS} days ago" +%Y-%m-%dT%H:%M:%SZ)
log "pruning base backups older than $cutoff"
mc rm --recursive --force \
  --older-than "${PG_BASE_KEEP_DAYS}d" \
  "${MC_ALIAS}/${OFFSITE_BUCKET#s3://}/base/" || true

rm -f "$artifact"
log "done"
