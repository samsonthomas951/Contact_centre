# Retention Schedule

Per Kenya DPA s.39: personal data must not be kept "for longer than is
necessary for the purposes for which it was processed". This document
maps each data class to a retention window and the code path that
enforces it.

| Data class | Default window | Source of truth | Enforced by |
|---|---|---|---|
| Customer message bodies | 24 months | `tenants.retention_messages_days` | Nightly partition drop on `messages` (forthcoming River job) |
| Document objects (post-ticket-close) | 30 days | `tenants.retention_documents_days` | `documents.retention_until` + reaper that consults `documents_purgeable` view, suspended by any active `document_holds` row |
| Audit ledger | 7 years | `tenants.retention_audit_days` | Monthly partition drop on `audit_events` (forthcoming) |
| Agent presence history | 90 days | hardcoded — Phase 1 | Trim job on `agent_status` (forthcoming) |
| Auth sessions / refresh tokens | 30 days | Zitadel-managed | Zitadel session policy |
| Webhook event-id dedupe rows | 24 hours | hardcoded — Phase 1 | River job (forthcoming) on `fb_webhook_events` and `x_webhook_events` |
| X spend telemetry (Prometheus counters) | 90 days | Prometheus retention | `--storage.tsdb.retention.time` |
| Application logs (Loki) | 30 days | Loki retention | Loki compactor config |

## Tenant overrides

Each tenant chooses values within these bands during onboarding. The
columns in `tenants` (`retention_messages_days`,
`retention_documents_days`, `retention_audit_days`) are the live
authoritative settings; the application reads from those, never from
hardcoded constants.

## Legal holds

Any record class can be exempted from the retention reaper by an
**active hold**. For documents, that's `document_holds.released_at IS
NULL`. For other classes, similar tables will be added when a customer
or regulator subpoenas a hold.

A hold blocks the reaper but does **not** block the right-to-erasure
fulfilment — DSR-driven deletes happen through the DSR playbook,
documented separately, which has its own evidentiary trail.

## Reaper architecture

A single nightly River job per record class:

1. SELECT a batch of purgeable rows (the `documents_purgeable` view is
   the canonical example).
2. For external storage (MinIO, etc.), DELETE the object first.
3. Mark the metadata row `deleted_at = now(), deleted_reason = 'retention'`.
4. Emit an `audit.events` entry: `data.purged` with the count.

The audit row stays forever — we log the erasure, never the data.
