# Breach Notification Runbook (Kenya DPA §43, 72-hour clock)

> **Statutory clock:** the moment the **Data Controller** (us, for
> agent data; the brand, for customer data) becomes *aware* of a
> personal-data breach, the 72-hour countdown to notify the ODPC
> begins. Processor → Controller notification SLA is **48 hours**.
>
> Failure to notify exposes us to fines up to **KES 5,000,000 or 1%
> of annual turnover (lower under the 2019 Act, "higher" under the
> pending 2025 Amendment Bill)**. Treat both numbers as live.

## Roles

| Role | Pager handle | Primary | Backup |
|---|---|---|---|
| Incident commander (IC) | `oncall-sre` | _named SRE rotation_ | _SRE lead_ |
| Data Protection Officer | `dpo` | _named DPO_ | _legal counsel_ |
| Communications | `comms` | _comms lead_ | CEO |
| Engineering | `eng-oncall` | _backend rota_ | _backend lead_ |

The IC owns the timeline; the DPO owns the regulator interface.

## Awareness triggers (any one starts the clock)

- Alertmanager fires on `webhook_signature_failure_total > 0` for >5
  minutes (signed webhook with bad signature → tampering attempt).
- `audit_chain_verify` job fails (hash chain broken).
- Unexpected DROP/UPDATE on `audit_events` (DB audit trail).
- `outbound_x_spend_usd` rate spikes 5× the per-tenant 30-day baseline
  (token compromise indicator).
- Any presigned-URL access from an IP outside the tenant's claimed
  agent-IP range.
- Customer or partner reports unauthorised disclosure.
- Internal review surfaces a breach during routine audit.

## Step-by-step (first 4 hours)

```
T+0:00  Awareness recorded by IC (Slack #ic-active or PagerDuty).
        IC opens an incident document (template below) and pages DPO.

T+0:15  IC + DPO confirm: is this a "breach of personal data" under
        s.2 DPA? If unclear, treat as breach until proven otherwise.

T+0:30  Engineering scopes blast radius:
          - which tenants?
          - which data subjects (count + categories)?
          - exfiltrated, altered, or merely accessible?
        Snapshot evidence: `audit_events`, NATS DLQ, MinIO access log,
        Postgres pg_stat_activity, container logs at -2h..now.

T+1:00  Containment decision: revoke tokens, rotate KEK, take affected
        connector offline, etc. **Document every action with timestamp
        and actor.** Do not delete logs.

T+2:00  DPO drafts the breach-notification entry for the ODPC portal.
        Required fields (per Reg 2021):
          - Nature of breach
          - Categories and approximate number of data subjects
          - Categories and approximate number of records
          - Likely consequences
          - Measures taken / proposed
          - Contact point (DPO name + email)

T+4:00  IC + DPO + Comms align on tenant + data-subject communications.
        Draft tenant email (we are processor; controllers must notify
        their users). Use the template in `breach-tenant-email.md`.
```

## ODPC notification (must complete within 72 hours of awareness)

1. Log into <https://www.odpc.go.ke> with the registered Data
   Controller account (credentials in Vault under
   `secrets/odpc/portal`).
2. Navigate to *Compliance → Breach reporting*.
3. Fill the breach form (the DPO drafted at T+2:00 above).
4. Upload supporting evidence: timeline, scope assessment, mitigations.
5. Save the case reference; record it in the incident document.

> If the 72-hour deadline cannot be met for legitimate reasons, file a
> *partial* notification with what you know and follow up. Better a
> partial-on-time than complete-late.

## Tenant notification (we are processor)

Per s.43(2), notify each affected controller (tenant) **within 48
hours** of awareness. Use `breach-tenant-email.md`. The controller is
then responsible for notifying *its* data subjects within their own
30-day SLA.

## Data-subject notification (when applicable)

Required only when the breach is "likely to result in high risk to
rights and freedoms" (s.43(4)). Examples that warrant direct user
notification:

- Plaintext password / token disclosure
- Sensitive personal data disclosure (national ID, health, financial)
- Re-identifiable behavioural profiles disclosed

The controller (tenant) sends; we provide the technical evidence and
the affected-list extraction.

## Post-incident

- **T+7d**: Internal post-mortem published. Blameless. Identify the
  root cause and the contributory factors.
- **T+14d**: Updated runbook(s) committed to this directory.
- **T+30d**: Tabletop exercise re-run for the team.
- **Annual review**: This runbook is reviewed by the DPO every 12
  months and after any actual incident, whichever is sooner.

## Incident document template

```
# Incident <YYYY-MM-DD-shortname>

## Summary
One-paragraph plain-English description.

## Timeline (times in UTC)
- HH:MM  Awareness — <how>
- HH:MM  IC paged
- HH:MM  DPO paged
- HH:MM  Containment action: <what>
- ...

## Scope
- Tenants affected:
- Data categories:
- Approximate records:
- Subjects identifiable from the disclosed data: yes/no

## Root cause
What broke.

## Contributing factors
What made it worse / harder to detect / harder to fix.

## Resolution actions taken
- [ ] Item, owner, due
- [ ] Item, owner, due

## ODPC reference
Case ID: ____
Filed at: <ts>

## Tenant notifications
- Tenant: notified at <ts>, ack at <ts>

## Lessons learned
Three short paragraphs.
```
