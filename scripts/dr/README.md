# Disaster Recovery scripts

Scripts and runbooks that implement the backup strategy described in
§11 of the technical plan:

> `pg_basebackup` weekly + WAL archiving to a separate object bucket
> every 5 min (PITR window: 7 days). MinIO versioning + lifecycle +
> nightly `mc mirror` to off-site bucket (preferably Kenya-region; see
> §13 on data localisation). Encrypted backups; restore drill quarterly.

## Scripts

| Script | Purpose | Run via |
|--------|---------|---------|
| `pg_basebackup.sh` | Take a base backup, encrypt with age, upload to off-site bucket. | Cron weekly (Sun 02:00 EAT). |
| `wal_archive.sh` | Postgres `archive_command` target — encrypts and pushes one WAL segment. | Postgres invokes per segment. |
| `minio_mirror.sh` | Snapshot `mc mirror` from primary MinIO to off-site bucket. | Cron nightly. |
| `restore_drill.sh` | One-shot: pull latest base + WAL, decrypt, restore into a throwaway PG, verify row counts and audit hash chain. | Manual / quarterly. |

## Conventions

- All scripts read configuration from `/etc/contactcentre/dr.env`
  (mode 0600, owned by `dr`). Required variables documented in
  `dr.env.example`.
- All artefacts are encrypted client-side with `age` keyed to the DR
  recipient. The recipient public key lives in the env file; the
  private key is held offline by the DPO. Compromising the off-site
  bucket alone yields no plaintext.
- Scripts are idempotent and re-entrant. Re-running with the same
  arguments either skips or overwrites; they never duplicate work.
- Exit non-zero on any failure; rely on the cron MAILTO to alert the
  operator.

## Restore SLO

| Scenario | Target |
|---|---|
| Single-table accidental drop | 30 min RTO, 5 min RPO via PITR |
| Whole-cluster loss | 2 h RTO, 5 min RPO |
| Region loss (off-site restore) | 4 h RTO, 1 h RPO (off-site replication lag) |

## Pre-go-live checklist

- [ ] dr.env populated with bucket + age public key
- [ ] `pg_basebackup.sh` runs end-to-end against staging
- [ ] `wal_archive.sh` accepts a forged WAL segment without erroring
- [ ] `restore_drill.sh` succeeds on a throwaway cluster
- [ ] DPO holds the age private key in a sealed envelope
