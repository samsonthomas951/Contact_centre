# Contact Centre

Multi-channel social-media contact-centre platform for Kenyan brands. Built in Go as a **modular monolith** with strictly bounded packages, deployed via Docker Compose on a single VPS, and architected to extract hot services to K3s/K8s when concrete scale thresholds are crossed.

## Channels

- Facebook / Messenger (Phase 0)
- X / Twitter (Phase 1)
- WhatsApp Business Cloud API (Phase 2)
- Instagram Business (Phase 2)
- Embeddable web widget (Phase 3)
- Voice (Phase 3)

## Stack

| Concern | Choice |
|---|---|
| Language | Go 1.23+ |
| HTTP | `chi` v5 |
| RPC | ConnectRPC |
| DB | PostgreSQL 16 + `pgx/v5` + `sqlc` |
| Migrations | `golang-migrate` / Atlas |
| Jobs | River (Postgres-backed) |
| Events | NATS JetStream |
| Cache / WS pubsub | Redis 7 |
| Object store | MinIO (S3-compatible) |
| AV | ClamAV |
| IdP | Zitadel (self-hosted) |
| Edge | Traefik v3 (TLS via Let's Encrypt) |
| Observability | OpenTelemetry → Prometheus / Loki / Tempo / Grafana |
| WebSockets | `coder/websocket` |

See [`contactcenter.md`](./contactcenter.md) for the full technical implementation plan.

## Repository layout

```
cmd/            entry points (one binary per future service)
internal/       private Go packages (bounded contexts)
api/            protobuf + OpenAPI schemas
deploy/         Docker Compose, Traefik, secrets templates
migrations/     SQL migrations (per logical schema)
scripts/        dev tooling
docs/           ADRs, runbooks, DPIA artefacts
```

## Compliance

Subject to the **Kenya Data Protection Act 2019** + General Regulations 2021. Pre-launch checklist:

- [ ] ODPC registration (Data Controller + Data Processor) — KES 4,000, 24-month validity
- [ ] DPO appointed
- [ ] DPIA completed and submitted (Section 31, 60 days before processing)
- [ ] Section 43 breach-notification runbook (72-hour ODPC notification)
- [ ] Records of Processing maintained
- [ ] Cross-border transfer documentation for Meta / X / WhatsApp

## Development

Requires Go 1.23+, Docker, Docker Compose v2, `make`.

```sh
make help
```
