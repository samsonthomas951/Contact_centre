# Data Protection Impact Assessment

> **Status:** Template — awaiting first formal completion before
> production traffic. **Owner:** DPO. **Review cycle:** annual + on
> material change. **Statutory basis:** Kenya DPA 2019 §31 + General
> Regulations 2021 r.21–22.

A DPIA is **mandatory** for this platform because we (a) process
personal data at scale, (b) systematically monitor data subjects (we
read inbound customer messages), and (c) may process sensitive data
(national ID images, financial complaints, health enquiries) shared by
customers in support tickets.

## 1. Description of processing

### 1.1 Nature, scope, context, purpose

| | |
|---|---|
| **Nature** | Storage and human review of customer messages received via Facebook Messenger, Facebook Page comments, X (Twitter) DMs and mentions, WhatsApp Business, Instagram Business, an embeddable web widget, and (Phase 3) inbound voice. Routing those interactions to a human agent who replies via the same channel. Long-term archival for service improvement, legal, and dispute resolution. |
| **Scope** | Up to ~100 agents per tenant; tens of thousands of customer interactions per month per tenant; multi-tenant SaaS where each brand is the data controller for *its* customers. Geographic scope: Kenya and the wider EAC region. |
| **Context** | The platform sits between a brand and its customers. Customers usually expect their post / DM to be read by *somebody* at the brand; what they don't expect is that the brand has a software vendor (us) in between. The privacy notice and consent flow must make this clear. |
| **Purpose** | Provide responsive customer support. Secondary: aggregated analytics (CSAT, AHT, SLA attainment) to improve service quality. **Not** for marketing without explicit, separate consent. |

### 1.2 Categories of data processed

| Category | Source | Lawful basis (DPA s.30) |
|---|---|---|
| Identifiers (name, handle, PSID, email, phone) | Customer message metadata | Contract (with brand) + Legitimate interest (service operation) |
| Free-text message content | Customer-supplied | Contract |
| Document attachments (PDF, DOC, DOCX) | Customer-uploaded | Contract |
| Possible sensitive data inadvertently shared (national ID images, health/financial info) | Customer-uploaded | **Explicit consent** (DPA s.45 — sensitive data needs explicit consent or another condition) |
| Agent identity, RBAC role, presence, performance metrics | Tenant employer | Employment contract + Legitimate interest |
| Audit ledger entries (every action) | System | Legal obligation (defensible audit per s.41) + Legitimate interest |
| X spend telemetry per tenant | API metering | Legitimate interest (cost control) |

### 1.3 Data flows

See `../../contactcenter.md` §1 for the architecture diagram. In DPA
terminology:

```
Customer (data subject)
   │  [message via Meta / X / WA / IG / widget / voice]
   ▼
Platform connector (we are processor; brand is controller)
   │  [normalised event on JetStream]
   ▼
Ticket service (we are processor)
   │  [routed to agent]
   ▼
Agent (acting on behalf of the brand)
   │  [reply back via the same channel]
   ▼
Customer
```

Sub-processors (each requires a DPA — `sub-processors.md`):

- Meta Platforms Inc. — Messenger / Instagram / WhatsApp transit
- X Corp. — X DM / post transit
- The VPS / colo provider hosting Postgres + MinIO + Redis (Phase 1)
- HashiCorp Vault for KEK custody (Phase 2)

## 2. Necessity and proportionality

| Question | Answer |
|---|---|
| Could we deliver the service with less data? | We could de-identify after a short window for analytics, but the support workflow requires identifying the customer to reply. Retention is the lever — see `retention.md`. |
| Have we minimised data collection? | We do not collect anything the channel doesn't already give us. The widget asks only for what the brand actually needs to triage. |
| How do data subjects exercise their rights? | Each brand exposes a contact route in its public privacy notice. Internally, the DSR playbook (`dsr-playbook.md`) defines a 30-day SLA per DPA s.26/s.40. |

## 3. Risks to data subjects

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R1 | Token theft → impersonation of brand to attack customers | Low | High | Tokens encrypted (envelope), Vault-managed; rotation runbook; audit alerts on outbound API calls outside business hours |
| R2 | Agent abuse — an internal user reads private DMs they shouldn't | Medium | Medium-High | Per-tenant + per-ticket access scoping; every read logged in `audit_events`; DPO can review by `actor_id` |
| R3 | AV bypass — malicious upload reaches an agent | Low | High | ClamAV INSTREAM scan + content-type allow-list before MinIO PUT; presigned URLs only for `scan_status='clean'` rows |
| R4 | Cross-border transfer to US (Meta/X) without basis | High (default state) | Medium | Document SCCs in Meta/X DPAs; explicit consent in privacy notice; Kenyan-region primary storage per Cloud Policy 2024 |
| R5 | Audit log tampered to hide an incident | Low | High | Hash-chained `audit_events` + Merkle anchors emailed daily to DPO; DB roles split between `audit_writer` and `audit_reader` (§8) |
| R6 | Sensitive data in tickets retained too long | Medium | Medium | Per-tenant retention windows; nightly reaper deletes beyond `retention_until`; legal-hold register for exceptions |
| R7 | Breach not noticed → 72h Section 43 deadline missed | Medium | High | Alertmanager rules on auth-failure + outbound-spend; `breach-runbook.md` with named owners and ODPC portal link |

Risk scoring is qualitative until we run a tabletop with the DPO; that
exercise produces the formal residual-risk score the ODPC reviewer
expects.

## 4. Cross-border transfer assessment {#cross-border}

Per DPA s.48, we currently rely on:

- **Standard contractual clauses** in our DPAs with Meta and X for the
  inevitable US transfer of message content carried by their APIs.
- **Explicit consent** in the public privacy notice for any other
  cross-border flow.

The Kenya–EU adequacy dialogue announced 7 May 2024 has not concluded;
do *not* assume EU adequacy when designing.

## 5. Consultation

If residual risk after mitigation is **high**, DPA s.31(7) requires
prior consultation with the Data Commissioner. The DPO triggers this
via the ODPC portal at <https://www.odpc.go.ke>.

## 6. Sign-off

| Role | Name | Date | Signature |
|---|---|---|---|
| DPO | | | |
| Engineering lead | | | |
| Legal | | | |
| Executive sponsor | | | |
