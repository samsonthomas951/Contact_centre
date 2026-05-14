# Deployment — Phase 1 (Docker Compose on a single VPS)

Per the technical plan §11, Phase 1 runs the full stack on one beefy VPS
(target sizing: 8 vCPU / 32 GB RAM / 500 GB NVMe for 100 agents).

## Components

| Container | Role | Network |
|---|---|---|
| `traefik` | Edge — TLS via Let's Encrypt (`tlschallenge`), HTTP→HTTPS redirect, metrics | `edge` |
| `postgres` | System of record (tickets, conversations, messages, audit, Zitadel) | `data` |
| `pgbouncer` | Transaction-mode pooling (5 000 client conns → 50-backend pool) | `data` |
| `redis` | Sessions, rate-limit counters, WS pub/sub, agent presence | `data` |
| `nats` | JetStream event bus (`ingress.*`, `outbound.*`, `audit.*`) | `data` |
| `minio` | S3-compatible document store (SSE-S3 AES-256, versioning) | `data` |
| `clamav` | INSTREAM virus scanning sidecar for document uploads | `data` |
| `zitadel` | Self-hosted OIDC IdP for agent SSO + MFA | `edge`, `data` |

The `data` network is `internal: true` — nothing on it is reachable from the host.
Only Traefik and (later) the Gateway and connector webhooks attach to `edge`.

## First-run

1. Generate secrets (see `secrets/README.md`).
2. `cp .env.example .env` and edit hostnames / ACME email.
3. From the repo root: `make up`.
4. Wait for `pg_isready` and Zitadel init to complete (~60 s on first boot).

## Backups (Phase 1)

- `pg_basebackup` weekly + WAL archiving every 5 min — PITR window 7 days.
- MinIO versioning + nightly `mc mirror` off-site (preferably Kenya-region per
  the Cloud Policy Dec 2024 localisation guidance).
- Quarterly restore drill — required by Kenya DPA §25 (security measures).

## Migration trigger to K3s/K8s

Promote to K3s when any of:

- single-VPS CPU > 70 % sustained,
- one Postgres tenant > 50 GB,
- > 50 concurrent agents,
- second large tenant onboards.
