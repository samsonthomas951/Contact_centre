#!/usr/bin/env bash
# Nightly mirror of the primary MinIO docs bucket to the off-site bucket.
# We rely on mc mirror's --remove flag to keep destination in sync with
# source (so deletions propagate) AND --preserve to keep upload-time
# metadata (content-type, sha256, agent-id headers).
#
# Cron entry (02:30 EAT, in UTC):
#   30 23 * * * /opt/contactcentre/dr/minio_mirror.sh

set -euo pipefail

ENV_FILE="${ENV_FILE:-/etc/contactcentre/dr.env}"
# shellcheck disable=SC1090
. "$ENV_FILE"

log() { printf '%s minio_mirror: %s\n' "$(date -u +%FT%TZ)" "$*"; }

mc alias set "$PRIMARY_MC_ALIAS" "$PRIMARY_MC_ALIAS_URL" \
  "$PRIMARY_MC_ACCESS_KEY" "$PRIMARY_MC_SECRET_KEY" >/dev/null
mc alias set "$MC_ALIAS" "$MC_ALIAS_URL" \
  "$MC_ACCESS_KEY" "$MC_SECRET_KEY" >/dev/null

src="${PRIMARY_MC_ALIAS}/${PRIMARY_BUCKET}"
dst="${MC_ALIAS}/${OFFSITE_BUCKET#s3://}/minio/${PRIMARY_BUCKET}"

log "mirror $src -> $dst"
mc mirror --quiet --remove --preserve "$src" "$dst"

# Lifecycle: keep MINIO_MIRROR_KEEP_DAYS days of object versions on
# the off-site bucket. The bucket itself has versioning enabled; the
# rule expires noncurrent versions and the deleted-marker tombstone.
log "applying lifecycle: expire noncurrent versions > ${MINIO_MIRROR_KEEP_DAYS}d"
cat <<EOF | mc ilm import --json - "${MC_ALIAS}/${OFFSITE_BUCKET#s3://}/minio/${PRIMARY_BUCKET}" >/dev/null
{ "Rules": [{
    "ID": "expire-noncurrent",
    "Status": "Enabled",
    "NoncurrentVersionExpiration": { "NoncurrentDays": ${MINIO_MIRROR_KEEP_DAYS} }
}] }
EOF

log "done"
