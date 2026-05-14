# Sub-Processors Register

Per Kenya DPA s.41 + General Regulations 2021 r.20, the controller
must maintain a register of every third party that processes personal
data on its behalf. This is ours.

| Sub-processor | Service provided | Data categories | Region | Legal basis | DPA in place? |
|---|---|---|---|---|---|
| Meta Platforms Inc. | Messenger / Page comments / Instagram / WhatsApp transit + storage of message thread state | Message content, customer identifiers (PSID, IGSID, WA phone), metadata | US (with global edge) | SCCs in Meta DPA + explicit user consent (privacy notice) | ⬜ pending DPA execution |
| X Corp. | DM / mention / reply transit, AAA webhook delivery | Message content, X user id + handle | US | SCCs in X DPA | ⬜ pending |
| Liquid Telecom (or chosen Kenyan colo) | Bare-metal hosting for Postgres + MinIO + Redis (Phase 1 single-VPS) | All persisted data | Kenya | Hosting agreement; physical and logical access controls audited annually | ⬜ pending |
| HashiCorp Vault | KEK custody (Phase 2 onward) | Encryption keys (no plaintext data) | Self-hosted | N/A — we operate it ourselves | n/a |
| Let's Encrypt | TLS certificate issuance | Domain names | US-based CA | Standard ACME terms | n/a |
| Africa's Talking (Phase 3, voice) | Voice termination + recording transit | Call audio + caller MSISDN | Kenya | Africa's Talking DPA | ⬜ Phase 3 |

## Operational rule

**Adding any new third party that touches personal data requires a DPO
sign-off and an entry in this table before traffic is enabled.** No
exceptions — even for "trial" integrations.

## Annual review

Each row above is reviewed every 12 months for:

- Continued necessity (do we still use them?)
- Contractual currency (is the DPA still in force?)
- Adequacy (have any rulings changed the legal-transfer landscape?)
- Sub-processor changes (have they added their own sub-processors?)
