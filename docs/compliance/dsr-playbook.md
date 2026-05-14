# Data Subject Request Playbook

Kenya DPA s.26 grants every data subject the right to request access,
rectification, erasure, restriction, portability, and objection. The
controller has **30 days** to respond (s.26(7)).

In our SaaS posture:

- The **brand** is the controller for *its* customers; the DSR enters
  *their* support pipeline first. Our role is to give them the tools.
- We are the **controller** for our agents' employment data.
- We are the **processor** for everything else.

## Routing matrix

| Request from | Controller | Our role | Channel |
|---|---|---|---|
| End customer of brand X | Brand X | Provide data extract / deletion API to Brand X | Brand X handles their user; we fulfil *their* technical request |
| One of our agents | Us | Direct response | `dpo@example.co.ke` |
| Anyone else | depends — assess | depends | DPO triages within 5 days |

## Per-right SOPs

### Access (s.26(1)(a))

1. Verify identity. For end customers, the brand verifies first; we
   accept the brand's verification.
2. Build extract: `audit_events` filtered by `actor_id` + `resource_id`,
   plus `messages`, `documents` (metadata only), `customers` row.
3. Deliver via signed link (presigned, 24h TTL) or per the brand's
   chosen channel.
4. Log a `dsr.access.fulfilled` row in `audit_events`.

### Erasure / Right to be forgotten (s.40)

> Note: erasure is **not** absolute. Legal-hold rows in
> `document_holds` and audit-retention obligations override.

1. Identify the data: customer id, all conversations, all tickets, all
   document object keys, all audit rows.
2. Documents → call Doc Svc Delete (MinIO RemoveObject + metadata
   `deleted_at`).
3. Customer + conversations + tickets → tombstone the customer row
   (NULL out PII, keep the id for referential integrity), redact
   message bodies (replace `body` with `[ERASED at <ts>]`).
4. Audit rows referring to that customer **stay** — we log the
   erasure, not the data. The audit row gains a `dsr.erasure.fulfilled`
   entry.
5. Inform the brand within 7 days that the request is complete.

### Rectification (s.26(1)(b))

Direct UPDATE on the relevant row(s). Must be logged in
`audit_events` with the before/after JSON in the payload.

### Portability (s.26(1)(d))

JSON export of the customer's tickets + messages + document hashes
(not bytes — signed presigned URLs to the actual files).

### Objection / restriction (s.26(1)(c) + s.26(1)(e))

Set tenant-side flag stopping further outbound; do not delete history.

## Statutory clock

Each request enters the DSR queue with a 30-day timer. The DPO
reviews the queue weekly. Anything within 7 days of the deadline is
escalated.
