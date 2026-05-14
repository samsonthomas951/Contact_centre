# Compliance — Kenya Data Protection Act 2019

This directory holds the compliance artefacts that the Office of the
Data Protection Commissioner (ODPC) expects to see during an audit, and
the runbooks the operations team executes when something goes wrong.

The technical implementation plan §13 maps each requirement below to a
section of code; this directory holds the *paperwork*.

## Pre-launch checklist

| Item | DPA reference | File | Owner | Status |
|---|---|---|---|---|
| Data Controller registration | DPA s.18 / Regs 2021 r.21 | `odpc-registration.md` | DPO | ⬜ pending |
| Data Processor registration | DPA s.18 / Regs 2021 r.21 | `odpc-registration.md` | DPO | ⬜ pending |
| Data Protection Officer appointed | DPA s.24 | `dpo-appointment.md` | CEO | ⬜ pending |
| Records of Processing Activities (ROPA) | Regs 2021 r.20 | `ropa.md` | DPO | ⬜ in progress |
| Data Protection Impact Assessment | DPA s.31 | `dpia.md` | DPO + Eng | ⬜ in progress |
| Privacy notice | DPA s.29 | `privacy-notice.md` | Legal + DPO | ⬜ pending |
| Breach-notification runbook (72h) | DPA s.43 | `breach-runbook.md` | SRE + DPO | ✅ drafted |
| Data Subject Request playbook | DPA s.26 + s.40 | `dsr-playbook.md` | Support + DPO | ⬜ pending |
| Cross-border transfer assessment | DPA s.48 | `dpia.md#cross-border` | DPO + Legal | ⬜ pending |
| Sub-processor register (Meta, X, …) | Regs 2021 r.20 | `sub-processors.md` | DPO | ⬜ pending |

These artefacts must be reviewed annually and within 30 days of any
material change to the platform's processing activities.

## Files

- `dpia.md` — Data Protection Impact Assessment template (DPA s.31).
- `breach-runbook.md` — 72-hour ODPC notification runbook (DPA s.43).
- `ropa.md` — Records of Processing Activities, mapped to controllers.
- `retention.md` — Per-data-class retention schedule and the code path
  that enforces it.
- `sub-processors.md` — Third parties (Meta, X, AWS/MinIO host, etc.)
  used in service delivery and the legal basis for each transfer.
- `dsr-playbook.md` — Data Subject Request response procedure (s.26 +
  s.40) including the 30-day statutory window.

## Pending regulatory developments

Track these — the legal regime is still moving:

- **Data Protection (Amendment) Bill, 2025** — proposes higher
  penalties (flipping "or whichever is *higher*"), broadens
  complainant standing to "any person", adds a Data Protection
  Appeals Tribunal (ss.64A–64F).
- **Draft Compliance Audit Regulations, 2024** — formalises the
  audit cadence ODPC inspectors will follow.
- **Kenya–EU Adequacy Dialogue** — opened 7 May 2024 in Nairobi but
  not concluded as of the plan's drafting (May 2026). Don't architect
  on the assumption of EU adequacy.

ODPC determinations are published at <https://www.odpc.go.ke/determinations/>;
read the latest before each annual review.
