# Kubernetes deployment (Phase 2 migration target)

Per §11 of the technical plan, Compose → K3s is the next promotion step
when any of the following thresholds are crossed:

- Single-VPS CPU > 70% sustained
- One Postgres tenant > 50 GB
- > 50 concurrent agents
- A second large tenant onboards

This directory holds Helm charts for that promotion. Charts are kept
minimal — one chart per service, dependencies pulled from the official
upstream charts.

## Layout

```
deploy/k8s/
├── README.md                  ← this file
├── charts/
│   ├── gateway/               ← cmd/gateway as a Deployment
│   ├── ws/                    ← cmd/ws as a Deployment
│   └── platform/              ← umbrella chart with upstream sub-charts
│       └── Chart.yaml
└── values/
    ├── staging.values.yaml
    └── prod.values.yaml
```

## Upstream dependencies

| Component      | Chart                                | Source |
|----------------|--------------------------------------|--------|
| Postgres       | `cnpg-operator` + `Cluster`          | CloudNativePG |
| Redis          | `redis` (Bitnami)                    | bitnami/redis |
| NATS JetStream | `nats`                               | nats-io/k8s |
| MinIO          | `minio-operator` + `Tenant`          | MinIO |
| Traefik        | `traefik` Ingress                    | traefik/traefik-helm-chart |
| External Secrets | `external-secrets`                 | external-secrets/external-secrets |
| Zitadel        | `zitadel`                            | zitadel/zitadel-charts |

Each is referenced from the umbrella `platform/Chart.yaml` with a
pinned version; the umbrella values file overrides the defaults that
matter for our deployment.

## Promotion sequence

1. Stand up K3s on three nodes (single-binary; embedded etcd
   alternative).
2. Install External Secrets Operator and configure it with the
   Vault instance the Phase-1 stack already uses.
3. `helm dependency build` then `helm install platform charts/platform`
   with the appropriate values file.
4. Install our service charts (gateway, ws) pointing at the platform
   release.
5. Cut DNS over once `readyz` passes on every replica.

## Horizontal scaling

Horizontal Pod Autoscaler v2 on:

- gateway: CPU 70%, replicas 2..6
- ws: custom metric `ws_connections` from Prometheus Adapter,
  target 5k connections/replica, replicas 2..10

## Secret model

No secret values live in this directory. Every chart references a
secret via `secretKeyRef`; External Secrets Operator pulls from Vault
and renders the Kubernetes `Secret`.

## Production hardening checklist

- [ ] All workloads run as non-root (`securityContext.runAsNonRoot: true`)
- [ ] All filesystems read-only (`readOnlyRootFilesystem: true`) except
      explicit emptyDir mounts
- [ ] NetworkPolicies allow only required egress (PG/Redis/NATS/MinIO/
      Zitadel; the rest -> Internet via an egress proxy)
- [ ] PodDisruptionBudgets: minAvailable=1 for every Deployment
- [ ] Image scan in CI blocks HIGH/CRITICAL CVEs (already in
      `.github/workflows/ci.yml`)
- [ ] Cosign signature verified by the cluster's admission policy
