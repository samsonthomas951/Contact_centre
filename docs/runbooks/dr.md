# DR runbook

Implements the recovery scenarios in `scripts/dr/README.md`. Operator
qualifications: SRE on-call rotation; access to the DPO-held age
identity for the off-site bucket.

## Scenarios

### S1 — Single-table accidental drop

**Symptom:** an admin ran `DROP TABLE` or a `DELETE` cleared a
table; the team needs the rows from before the bad operation.

**Recovery (PITR):**

1. Identify the LSN or timestamp just before the bad operation
   (`SELECT pg_current_wal_lsn(); -- on the running cluster` or
   the application log line preceding it).
2. On a *separate* host (not the primary), create an empty staging PG.
3. Restore the latest base backup with `restore_drill.sh` adapted to
   the staging host (set `STAGING_PG_PORT=5432`).
4. Add `recovery_target_time = '<ISO ts>'` to
   `postgresql.auto.conf` of the staging cluster and add a
   `restore_command` that fetches WAL from the off-site bucket:
   ```
   restore_command = '/opt/contactcentre/dr/wal_restore.sh "%f" "%p"'
   recovery_target_time = '2026-05-14 10:42:00 EAT'
   recovery_target_action = 'pause'
   ```
5. Start the staging cluster; wait for it to pause at the target.
6. Dump the target table from staging:
   `pg_dump -t public.tickets staging_db | psql primary_db`.
7. Promote staging when row counts and a spot-check pass.

**SLO:** 30 min RTO, 5 min RPO (one WAL segment).

### S2 — Whole-cluster loss

**Symptom:** primary VPS or PG data directory unrecoverable.

**Recovery:**

1. Provision a replacement host (or VM, or new K3s node).
2. Run `restore_drill.sh` against the latest base + all WAL.
3. Promote the cluster (`pg_ctl promote`).
4. Point pgbouncer / app at the new primary.
5. Re-sync MinIO from off-site (`mc mirror offsite/.../minio/docs primary/docs`).
6. Smoke-test: open one ticket, send one outbound, verify audit chain.

**SLO:** 2 h RTO, 5 min RPO.

### S3 — Region loss (off-site failover)

**Symptom:** the entire Kenya-region data centre is unreachable.

**Recovery:**

1. Activate the off-site Kenya-region replica (cold standby per the
   plan's localisation note in §13).
2. Run S2 there.
3. Update DNS for app.example.co.ke and ws.example.co.ke to the
   replica VIP. Wait the configured TTL (5 min in production).
4. Brand-team comms via the agreed channel.

**SLO:** 4 h RTO, 1 h RPO (off-site replication lag).

## Quarterly drill

Run S2 against staging on the first business day of each quarter.
Record outcomes in `docs/runbooks/dr-drills/` (a per-quarter log file).
Capture: actual RTO, actual RPO, any deviation from this runbook,
follow-up actions.

A drill that doesn't end in a working cluster blocks the next release.

## Encryption keys

- All backup artefacts are `age`-encrypted at write time.
- The recipient public key (`age1...`) lives in `dr.env` on the
  backup hosts.
- The corresponding **private** key is held by the DPO in a sealed
  envelope. Two-person rule: the DPO and one of (CEO, CTO) must be
  present to open the envelope.
- Rotation: annually, or on any incident touching the backup hosts.
