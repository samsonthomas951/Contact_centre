# Migrations

Plain-SQL migrations applied with [`golang-migrate`](https://github.com/golang-migrate/migrate).
Each step has matching `*.up.sql` and `*.down.sql` files; CI runs the up then
the down then the up again to verify reversibility.

| # | Subject |
|---|---|
| 0001 | Extensions, `set_updated_at()` helper, `tenants` |
| 0002 | `agents`, `agent_skills`, `agent_status` |
| 0003 | `customers`, `conversations` |
| 0004 | `tickets`, partitioned `messages`, `assignments` |
| 0005 | Hash-chained partitioned `audit_events`, `audit_anchors`, `audit_writer`/`audit_reader` roles |
| 0006 | `fb_pages` token vault, `fb_webhook_events` dedupe |
| 0007 | `x_accounts` token vault (OAuth1 + OAuth2 PKCE), `x_webhook_events` dedupe |
| 0008 | `documents` metadata, `document_holds` (legal-hold suspension of retention), `documents_purgeable` view |
| 0009 | `wa_phone_numbers` token vault, `wa_templates` approved-template registry, `wa_webhook_events` dedupe |
| 0010 | `ig_accounts` Path-1/Path-2 token vault, `ig_webhook_events` dedupe |

## Running

```sh
export DATABASE_URL=postgres://contactcentre:PASS@localhost:5432/contactcentre?sslmode=disable
make migrate
```

## Conventions

- One concept per migration; never edit a merged file — always add a new one.
- Multi-tenant tables carry `tenant_id UUID NOT NULL REFERENCES tenants(id)`.
- Timestamps are `TIMESTAMPTZ` and default to `now()` (or `clock_timestamp()`
  inside transactions where wall-clock matters — `audit_events` uses the
  latter so two events in the same tx have monotonic `ts`).
- `messages` and `audit_events` are RANGE-partitioned by `created_at` / `ts`;
  a default partition catches anything outside known ranges. A scheduled job
  rotates monthly partitions ahead of time and drops past-retention ones.
- Audit roles `audit_writer` / `audit_reader` enforce separation of duties
  per §8 of the technical plan.
