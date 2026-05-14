#!/usr/bin/env bash
# Postgres archive_command target. Encrypts one WAL segment with age
# and uploads to the off-site bucket.
#
# Configure postgresql.conf:
#   archive_mode = on
#   archive_command = '/opt/contactcentre/dr/wal_archive.sh "%p" "%f"'
#   archive_timeout = 300            # force segment every 5 min
#
# Postgres re-invokes the command on non-zero exit, so failures don't
# silently lose WAL. We log to a fixed file under /var/log so a
# misbehaving cron MAILTO doesn't drown the operator -- the daemon
# itself surfaces archive_command failures via pg_stat_archiver.

set -euo pipefail

ENV_FILE="${ENV_FILE:-/etc/contactcentre/dr.env}"
# shellcheck disable=SC1090
. "$ENV_FILE"

src="${1:?wal_archive: source path required}"   # absolute path from %p
name="${2:?wal_archive: segment name required}" # filename from %f

log() {
  printf '%s wal_archive: %s\n' "$(date -u +%FT%TZ)" "$*" \
    >> /var/log/contactcentre-wal-archive.log
}

# Sanity: the file must exist.
if [[ ! -f "$src" ]]; then
  log "missing $src"
  exit 2
fi

key="wal/$(date -u +%Y%m%d)/${name}.age"
log "archiving $name -> $key"

mc alias set "$MC_ALIAS" "$MC_ALIAS_URL" "$MC_ACCESS_KEY" "$MC_SECRET_KEY" >/dev/null

# Stream encrypt + upload in one shot; no on-disk artifact.
if ! age -r "$AGE_RECIPIENT" < "$src" \
     | mc pipe --quiet "${MC_ALIAS}/${OFFSITE_BUCKET#s3://}/${key}"; then
  log "upload failed for $name"
  exit 3
fi

log "ok $name"
