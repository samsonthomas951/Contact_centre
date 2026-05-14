# Secrets (Phase 1: Docker secrets)

These files are mounted into containers via Docker secrets. They are **gitignored** —
generate fresh ones per environment and never commit real values.

| File | Purpose |
|---|---|
| `pg_password` | Postgres superuser/owner password (also used by Zitadel and pgbouncer) |
| `minio_root_user` | MinIO root username |
| `minio_root_password` | MinIO root password (≥ 8 chars) |
| `zitadel_masterkey` | Zitadel master encryption key — exactly **32 bytes** |

## Generate locally

```sh
cd deploy/secrets
umask 077
openssl rand -base64 24 | tr -d '\n' > pg_password
echo -n "admin"                       > minio_root_user
openssl rand -base64 24 | tr -d '\n' > minio_root_password
openssl rand -base64 32 | head -c 32 > zitadel_masterkey
```

When migrating to K8s, replace this directory with **External Secrets Operator**
fed from HashiCorp Vault per the deployment plan.
