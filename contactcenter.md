# Technical Implementation Plan — Multi-Channel Social Media Contact Center Platform (Go, Kenya)

## TL;DR
- Build as a **modular monolith first** with strictly bounded packages (auth, ticket, audit, document, connector-fb, connector-x), deployed via Docker Compose on a single VPS, and extract services later. Choosing microservices on day one for a small team is the single biggest risk to delivery.
- **Recommended stack:** Go 1.23+ with **Chi** (HTTP) + **ConnectRPC** (internal), **pgx/v5 + sqlc** (Postgres), **River** (Postgres-backed jobs), **NATS JetStream** (event bus), **Redis** (cache + WS fan-out), **MinIO** (S3-compatible documents), **Zitadel** (Go-native self-hosted IdP), **coder/websocket**, **OpenTelemetry + Prometheus + Loki + Tempo**, **Traefik** (TLS), **ClamAV** (AV).
- **Compliance posture:** Register with the Office of the Data Protection Commissioner (ODPC) before go-live (KES 4,000 fee, 24-month validity); implement the 72-hour Section 43 breach process; complete a DPIA before launch (Kenya DPA 2019 + General Regulations 2021 + pending Amendment Bill 2025); be aware X API moved to pay-per-use in February 2026 and that read endpoints are capped at 2M/month outside Enterprise — design the X connector for write-heavy use.

---

## 1. High-Level System Architecture

```
                          ┌──────────────────────────────────┐
   Customer ──┐           │           Edge Layer             │
   (FB/X/WA/  │  HTTPS    │  Traefik (TLS, HTTP/2, mTLS opt) │
   IG/Widget) ├──────────▶│  WAF rules, rate-limit, IP allow │
              │           └────────────┬─────────────────────┘
   Agent UI ──┘                        │
   (React)                             ▼
                          ┌──────────────────────────────────┐
                          │       API Gateway (Go/Chi)       │
                          │  AuthN (JWT verify), AuthZ (RBAC)│
                          │  Request fan-out, OpenTelemetry  │
                          └─┬───────────┬───────────┬────────┘
                            │ gRPC      │ gRPC      │ WS
            ┌───────────────┼───────────┼───────────┼──────────────┐
            ▼               ▼           ▼           ▼              ▼
   ┌────────────┐  ┌──────────────┐ ┌────────┐ ┌──────────┐ ┌────────────┐
   │ Auth/IAM   │  │ Ticket Svc   │ │ Doc Svc│ │ Realtime │ │ Connectors │
   │ Zitadel +  │  │ Conversations│ │ MinIO+ │ │ WS Svc   │ │ FB / X /   │
   │ agent dir. │  │ Routing      │ │ ClamAV │ │ presence │ │ WA / IG /  │
   └─────┬──────┘  └──────┬───────┘ └───┬────┘ └────┬─────┘ │ Widget     │
         │                │             │           │       └──────┬─────┘
         │  ┌─────────────┴─────────────┴───────────┴──────────────┘
         │  │
         ▼  ▼
   ┌─────────────────────┐   ┌──────────────────┐   ┌────────────────────┐
   │ PostgreSQL 16       │   │ Redis 7          │   │ NATS JetStream     │
   │ (per-svc schemas)   │   │ cache, WS pubsub,│   │ events, retries,   │
   │ pgbouncer in front  │   │ rate-limit ctrs  │   │ DLQ                │
   └─────────────────────┘   └──────────────────┘   └────────────────────┘

   ┌─────────────────────┐   ┌──────────────────┐   ┌────────────────────┐
   │ MinIO (S3-compat)   │   │ ClamAV (clamd)   │   │ Observability      │
   │ AES-256, SSE-S3     │   │ stream scan API  │   │ Prom/Loki/Tempo/   │
   │ versioning, lifecyc │   │ freshclam daemon │   │ Grafana/Alertmgr   │
   └─────────────────────┘   └──────────────────┘   └────────────────────┘
```

**Boundaries (logical first, physical later):**
- **Inbound platform events** terminate in their respective Connector, which is the *only* code that talks to Meta/X. Connectors **normalize** payloads to an internal `InboundMessage` event published to NATS subject `ingress.<channel>.message`.
- **Ticket Service** is the system of record for tickets, conversations, messages, and routing decisions.
- **Doc Service** owns object storage, encryption, virus scanning, presigned URL minting; no other service writes to MinIO.
- **Realtime WS Service** is stateless; it subscribes to Redis pub/sub channels keyed by ticket/agent and pushes to connected browsers.
- **Auth Service** = Zitadel + a thin Go shim that mints internal access tokens after Zitadel SSO.
- **Audit Service** is the only consumer that writes the append-only audit ledger. Every other service publishes audit events to `audit.events`.

---

## 2. Microservice Breakdown

| Service | Responsibility | Owns | Exposes | Publishes / Consumes |
|---|---|---|---|---|
| **API Gateway** | TLS termination, JWT verify, RBAC, request routing, rate limit | — | REST (public), WS upgrade | — |
| **Auth/Identity** | Agent login (Zitadel OIDC), token issuance, MFA, RBAC roles | `users`, `roles`, `sessions` (Zitadel) | OIDC, /me, /refresh | pub `audit.login` |
| **Agent Management** | Skills, availability presence, work capacity | `agents`, `skills`, `agent_status` | gRPC: `GetEligibleAgents` | sub `presence.*` |
| **Ticket / Conversation** | Ticket CRUD, state machine, SLA timers, message store | `tickets`, `conversations`, `messages`, `assignments` | gRPC + REST | pub `ticket.created/assigned/closed`; sub `ingress.*` |
| **Routing Engine** | Hybrid skill+priority+least-busy algorithm | (uses Agent + Ticket) | gRPC: `Route(ticketID)` | sub `ticket.created`; pub `ticket.assigned` |
| **Connector: Facebook** | Page webhook intake, signature verify (X-Hub-Signature-256), Graph API client, Page token vault | `fb_tokens`, `fb_webhook_subs` | webhook `/v1/fb/webhook` | pub `ingress.fb.*`; sub `outbound.fb.*` |
| **Connector: X** | Account Activity API webhook (CRC), v2 API for replies, pay-per-use cost tracking | `x_tokens`, `x_aaa_envs` | webhook `/v1/x/webhook` | same pattern |
| **Connector: WhatsApp** (phase 2) | Cloud API webhook, template approvals | `wa_phone_ids`, `wa_templates` | `/v1/wa/webhook` | same |
| **Connector: Instagram** (phase 2) | IG Business via Graph; reuses FB token model | `ig_accounts` | `/v1/ig/webhook` | same |
| **Connector: Widget** | Embedded JS widget backend; tickets created over WS | `widget_sites` | WS + REST | pub `ingress.widget.*` |
| **Document Service** | Upload, AV scan, encryption, presigned URLs, retention | MinIO + metadata in PG | gRPC: `Upload`, `GetURL`, `Delete` | sub `ticket.closed` (lifecycle) |
| **Realtime / WebSocket** | Per-agent push (new message, assignment, presence) | none (stateless) | WS `/ws/agent` | sub Redis `ws.agent.<id>` |
| **Notification** | Email/SMS/push (escalations, breach alerts) | `notif_queue` | gRPC | sub `ticket.sla_breach`, etc. |
| **Audit** | Append-only ledger, hash-chained, searchable | `audit_events` (partitioned) | gRPC: `Query`, `Verify` | sub `audit.*` |
| **Analytics** | OLAP rollups (CSAT, AHT, FCR, SLA) | `mart_*` tables (PG) | REST | sub `ticket.*`, `message.*` |

Per-service tech: **all Go**, **Chi** for HTTP, **connectrpc.com/connect** or **grpc-go** for internal RPC, **sqlc-generated** queries against **pgx/v5**, **slog** for logs, **OpenTelemetry SDK** for traces/metrics, **River** for in-service background work.

---

## 3. Recommended Go Stack

| Concern | Pick | Why |
|---|---|---|
| HTTP framework | **chi v5** | Per JetBrains' "The Go Ecosystem in 2025" report (Nov 10, 2025), based on the State of Developer Ecosystem Report 2025 (24,534 developers, April–June 2025), Gin leads at 48% use, Echo 16%, Fiber 11%, but Chi is preferred for microservices because it is "fully net/http compatible" — any stdlib middleware works, important when you have 8+ services. Use Gin for the *agent BFF* if you want batteries-included. |
| Inter-service RPC | **ConnectRPC** (connectrpc.com/connect) | gRPC wire-compatible, but speaks HTTP/1.1 + HTTP/2 + gRPC-Web from the same handler — eliminates a separate envoy/grpc-gateway. |
| OAuth2 client | **golang.org/x/oauth2** + per-provider package | Standard. Use `oauth2.ReuseTokenSource` for refresh. |
| DB driver | **jackc/pgx v5** via `pgxpool` | Native PG binary protocol. Per Phil Eaton's October 2023 benchmark (notes.eatonphil.com), lib/pq via database/sql runs at 95,665 rows/s on PostgreSQL insert-heavy workloads versus pgx native at 214,869 rows/s — i.e., the standard driver is 44–76% slower than pgx on inserts. |
| Query layer | **sqlc** (sql_package: pgx/v5) | Compile-time-safe SQL. Cloudflare-pattern; avoid GORM's reflection overhead for the high-volume message and audit paths. Use **Bun** only if the team strongly prefers a query builder. Do **not** introduce GORM. |
| Migrations | **golang-migrate** or **Atlas** | Atlas integrates better with sqlc workflows. |
| Background jobs | **River** (riverqueue.com) | Postgres-backed — transactional enqueue with your business writes; "If your API's transaction succeeds, your job will be enqueued—period." No Redis required for the job layer. Add **Asynq** only if you outgrow Postgres job throughput (~20k/s). |
| WebSockets | **coder/websocket** (formerly nhooyr/websocket) | Modern, context-aware, no goroutine-per-frame. Per WebSocket.org's Go WebSocket guide, gorilla/websocket's "original repository was archived in late 2022. The code still works…But bug reports go unanswered and security patches depend on community forks." The Gorilla toolkit archival date was December 9, 2022. |
| Validation | **go-playground/validator/v10** | De facto standard; works with Gin/Echo/Chi via struct tags. |
| Logging | **log/slog** (stdlib, Go 1.21+) | Per JetBrains 2025 trends report, slog is "the natural choice for starting fresh." Pipe JSON to Loki. |
| Config | **spf13/viper** + env-var first | 12-factor; mount secrets via Docker secrets / SOPS. |
| Observability | **OpenTelemetry Go SDK** + OTLP → Tempo (traces), Prometheus (metrics), Loki (logs) | Single instrumentation, swappable backends. |
| HTTP client | **net/http** + `Transport{MaxIdleConnsPerHost: 100}` + **circuit-breaker** (sony/gobreaker) | For Graph API / X API outbound. |
| Resilience | **cenkalti/backoff/v5** for exponential retry; **uber-go/ratelimit** for outbound throttling | |

---

## 4. Social Media API Integration Strategy

### 4.1 Facebook / Messenger / Instagram (Graph API)

**Required permissions (Meta Graph API v22+):**

For Page support: `pages_show_list`, `pages_read_engagement`, `pages_read_user_content` (mentions / customer comments), `pages_manage_metadata` (webhook subscriptions), `pages_messaging` (DMs), `pages_manage_engagement` (reply to comments), `pages_manage_posts`, `business_management`.

For Instagram (Path 1, Facebook-linked): `instagram_basic`, `instagram_manage_messages`, `instagram_manage_comments`. The newer Path 2 (`instagram_business_*`) supports direct Instagram login without a linked Page; build the connector to accept both flows.

The legacy `manage_pages` and `publish_pages` were removed in May 2022 and must not be referenced.

**Token model:** Short-lived user token → 60-day long-lived user token (`grant_type=fb_exchange_token`) → never-expiring **Page access token** via `GET /me/accounts`. Per Meta's official long-lived tokens documentation: *"Long-lived Page access tokens do not have an expiration date and only expire or are invalidated under certain conditions"* (admin removed, password change, ~90 days inactivity). Store these encrypted (envelope encryption, see §7); the server-side exchange "should never be made client-side" because the app secret is required.

**Webhook signature verification (CRITICAL):** Meta signs every webhook with HMAC-SHA256 using your App Secret. Header is `X-Hub-Signature-256: sha256=<hex>`. You MUST verify against the **raw** body before JSON parsing.

```go
func verifyMetaSignature(rawBody []byte, header, appSecret string) bool {
    mac := hmac.New(sha256.New, []byte(appSecret))
    mac.Write(rawBody)
    expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(header))
}
```

Common bug: middleware that parses JSON before signature check destroys the byte equivalence. In Chi, capture `io.ReadAll(r.Body)` first and restore via `r.Body = io.NopCloser(bytes.NewReader(buf))`.

**Webhook subscription fields:** `messages`, `messaging_postbacks`, `message_reads`, `message_deliveries`, `feed`, `mention`. Important: in Development Mode, `mention` and `feed` events do not reliably fire — they require Advanced Access (live app).

**App Review:** Multi-tenant SaaS for third-party brands requires **Advanced Access** for every permission above, which requires **Business Verification** (legal docs, tax ID) plus a **Tech Provider Access Verification** (~5 business days). Meta states App Review takes up to 10 business days but plan **4–8 weeks total**. `pages_read_user_content` is the most frequently denied — justify it explicitly as "support agents need to read inbound customer comments and mentions to respond."

**Rate limits — Business Use Case (BUC):** Page tokens hit BUC limits, not platform limits. Monitor the **`X-Business-Use-Case-Usage`** response header (sometimes labelled `X-Business-Use-Case`); it returns JSON keyed by business object ID with `call_count`, `total_cputime`, `total_time` as 0–100 percentages. Throttle when any hits 80. Watch for error codes **4, 17, 32, 613** (subcode 1996 = inconsistent traffic). For Instagram messaging: hard caps of **2 calls/sec/IG account** on the Conversations API and **100 calls/sec/IG account** on Send API (text). Meta does not publish closed-form quotas for `pages`/`messenger` BUC — instrument the headers in production.

### 4.2 X (Twitter) v2

**As of February 6, 2026, X replaced fixed tiers with pay-per-use as the default** for new developers. Existing Basic ($200/mo) and Pro ($5,000/mo) subscriptions remain available but cannot be newly purchased. Per Blotato's 2026 X API pricing guide, after the April 20, 2026 update:

- Standard write: **$0.015/post** — "Standard writes…went from $0.010 to $0.015 per request"
- Post containing a URL: **$0.20** — "Posts containing a URL jumped to $0.20 per request. That's a 1,900% increase over the original $0.010 write price"
- Read: $0.005 (or $0.001 for "owned reads" — your own posts/bookmarks/followers after the April 20, 2026 update)
- Cap: **2M reads/month** before Enterprise (~$50,000/mo, "50+ million Posts/month") is required.

**Architectural implication:** A high-volume social-listening tool is uneconomic on pay-per-use. A *brand-reply* contact center is feasible because reads are limited to the Account Activity webhook stream (free pushes) + occasional context fetches. Budget ~$0.015 × outbound replies + monitor monthly read cap. **Do not** build the X connector to poll for mentions; that will burn the read cap fast.

**Receiving inbound:** Use the **Account Activity API (AAA)** — webhook-based delivery of tweets, mentions, replies, retweets, quote tweets, and DMs sent to subscribed accounts. Authentication: **OAuth 1.0a** for user-specific subscribe calls; OAuth 2.0 App Bearer for management. Implement the **Challenge Response Check (CRC)**: X periodically sends a GET with a `crc_token`; you must respond with an HMAC-SHA256 over the token using your consumer secret. Failing CRC unregisters your webhook.

**OAuth scopes for outbound (v2):** OAuth 2.0 with PKCE; scopes `tweet.read`, `tweet.write`, `users.read`, `dm.read`, `dm.write`, `offline.access` (refresh tokens).

**Shared brand account architecture (X and Meta):** ONE set of platform credentials (the brand's Page/X account) is stored in the connector's token vault. Agents log into the contact-center system via Zitadel — they never authenticate directly to X/Meta. When an agent clicks "Reply," the connector authenticates to the platform using the brand's stored token, but the *internal* `audit_events` row records `agent_id`, `tenant_id`, `platform_message_id`, `outbound_text`, `correlation_id`. This is the industry standard pattern (Sprinklr, Hootsuite, Sprout, Zendesk Social do the same). The DPIA must call out that the brand is the data controller and your platform is the processor.

---

## 5. Authentication & Authorization

**Do not use Facebook/X OAuth as the agent login.** Agent identities must survive a platform credential rotation, support MFA, support deprovisioning when an employee leaves, and not be hijackable by a brand-account compromise. Social SSO for agents is a security anti-pattern.

**Recommended IdP: Zitadel** (self-hosted, Go-based, Apache-2.0). Per the official comparison, Zitadel is "API-first… event-sourced DB, built-in audit log & password-less support… single binary; multi-tenant out of the box." Alternative: Keycloak (most mature, but heavy JVM footprint and per Oso's 2025 Keycloak Alternatives guide, "Keycloak's configuration UI and documentation can feel overwhelming, especially for smaller teams or those new to IAM"). Ory Hydra is unbundled (auth-server only) and better only if you already use Kubernetes and want each microservice composable.

| Concern | Decision |
|---|---|
| Token format | **JWT access (5 min)** + **opaque refresh (30 days, rotated)** via Zitadel |
| Session model | Stateless JWT in API; revocation via short TTL + Redis denylist for emergencies |
| RBAC | Roles: `agent`, `senior_agent`, `supervisor`, `admin`, `auditor`, `dpo` |
| Permissions | Resource-based; checked at gateway and re-checked at service for defense in depth |
| MFA | TOTP mandatory for supervisor/admin/dpo/auditor; WebAuthn passkey supported |
| Social SSO (optional) | Only as a *secondary* identity link, never primary — Zitadel can broker FB/Google for convenience after first-time provisioning by an admin |
| Service-to-service | mTLS via SPIFFE-style certs (cert-manager later); JWTs signed by Zitadel with `aud` per service |

---

## 6. Ticket Lifecycle & Routing

**Core data model:**

```sql
CREATE TABLE customers (
  id UUID PRIMARY KEY,
  tenant_id UUID NOT NULL,
  external_refs JSONB NOT NULL DEFAULT '{}', -- {fb_psid:..., x_user_id:..., wa_phone:...}
  display_name TEXT, email TEXT, phone TEXT,
  created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE conversations (
  id UUID PRIMARY KEY,
  tenant_id UUID NOT NULL,
  customer_id UUID NOT NULL REFERENCES customers(id),
  channel TEXT NOT NULL CHECK (channel IN ('fb','x','wa','ig','widget')),
  channel_thread_id TEXT,  -- e.g., Messenger thread id or X DM convo id
  UNIQUE (tenant_id, channel, channel_thread_id)
);

CREATE TYPE ticket_state AS ENUM ('new','open','pending','on_hold','resolved','closed','reopened');

CREATE TABLE tickets (
  id UUID PRIMARY KEY,
  tenant_id UUID NOT NULL,
  conversation_id UUID NOT NULL REFERENCES conversations(id),
  state ticket_state NOT NULL DEFAULT 'new',
  priority SMALLINT NOT NULL DEFAULT 3,    -- 1=urgent .. 5=low
  required_skills TEXT[] DEFAULT '{}',
  assigned_agent_id UUID,
  sla_first_response_due TIMESTAMPTZ,
  sla_resolution_due TIMESTAMPTZ,
  created_at TIMESTAMPTZ DEFAULT now(),
  closed_at TIMESTAMPTZ
);
CREATE INDEX ON tickets (tenant_id, state, priority);
CREATE INDEX ON tickets (assigned_agent_id) WHERE state IN ('open','pending');

CREATE TABLE messages (
  id BIGSERIAL,
  ticket_id UUID NOT NULL,
  direction TEXT CHECK (direction IN ('in','out','note')),
  agent_id UUID,
  body TEXT,
  attachments UUID[],
  platform_message_id TEXT,
  created_at TIMESTAMPTZ DEFAULT now(),
  PRIMARY KEY (created_at, id)
) PARTITION BY RANGE (created_at);   -- monthly partitions

CREATE TABLE assignments (
  id BIGSERIAL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  agent_id UUID NOT NULL,
  from_agent_id UUID,
  reason TEXT,           -- 'auto_route', 'manual', 'escalation', 'reassign_offline'
  at TIMESTAMPTZ DEFAULT now()
);
```

**State machine:** `new → open → (pending | on_hold) → resolved → closed`; from `resolved/closed` can `reopened → open` on customer reply within retention window (e.g., 7 days for Messenger window, 24 h for X DM rules).

**Routing — hybrid algorithm:**

1. **Filter eligibility:** agents online, with `current_load < max_concurrent` (default 5), whose `skills ⊇ ticket.required_skills`.
2. **Score:** `score = priority_weight × ticket.priority + 0.3 × (max_concurrent - current_load) + 0.2 × freshness(last_assigned)` — implements priority-first, least-busy, round-robin tiebreaker (the Zendesk-recommended pattern: "If more than one agent has an eligible status and the same spare capacity for the relevant channel, the ticket is assigned to the agent who hasn't been assigned a ticket from the relevant channel in the longest time").
3. **Pick** highest score. If none, queue with TTL; **escalate** on SLA breach.
4. **SLA timers** are River scheduled jobs with retries; on breach, raise priority and re-route, plus notify supervisor.

**Presence model:** Redis hash `agent:status:{id}` with `status` (online/away/break/offline), `last_heartbeat`, `current_load`. WS heartbeat every 15s; auto-away after 90s missed.

**Capacity & re-balancing:** Background job every 30s scans `tickets WHERE state='open' AND assigned_agent_id IN (offline agents)` and re-routes via the same algorithm with reason `reassign_offline`.

---

## 7. Document Security (PDF / Word)

**Storage:** **MinIO** in the same Docker network (S3-compatible). Avoid filesystem-on-disk: lifecycle, replication, presigned URLs, encryption all come free.

**Encryption at rest:** MinIO **SSE-S3** with AES-256-GCM; KMS option is **HashiCorp Vault** or `age` for the VPS phase (Vault running locally; auto-unseal via cloud KMS when migrated). Envelope encryption: per-tenant DEK wrapped by master KEK in Vault.

**In transit:** TLS 1.3 everywhere; mTLS between services using a local CA (cert-manager once on K8s; manual issuance with `step-ca` for Compose). Traefik handles public TLS with ACME / Let's Encrypt.

**Virus scanning pipeline:**

```
client → Doc Svc /upload (multipart)
        → stream into tee:
            ├── ClamAV clamd via INSTREAM (TCP 3310 or unix socket)
            └── temp buffer (max 25 MB; spill to disk if larger)
        ← if FOUND → reject + audit `doc.malicious`
        ← if OK   → put to MinIO at s3://docs/{tenant}/{ulid}.bin
                     metadata: content_type, sha256, size, scanned_at
```

Use `dutchcoders/go-clamd` for INSTREAM. Run `freshclam` as a sidecar updating definitions daily.

**Content-type validation:** server-side detection (`net/http.DetectContentType` + magic-number check via `gabriel-vasile/mimetype`); reject anything not in allow-list `[application/pdf, application/msword, application/vnd.openxmlformats-officedocument.wordprocessingml.document]`. Strip macros from .docx by re-saving via `unioffice` or rejecting macro-enabled files outright.

**Access:** Documents are accessed only via **presigned URLs minted by Doc Svc** with **TTL ≤ 5 minutes** and bound to the agent's session (custom `x-amz-meta-agent-id` recorded in audit). Never expose direct MinIO URLs.

**Retention & right to erasure:** Per-tenant retention policy table; nightly job deletes objects past TTL. Right-to-erasure requests (Kenya DPA Section 40) hit Doc Svc which deletes object **and** writes an `audit.doc.erased` event referencing the now-broken hash chain link (you log the erasure, not the data).

---

## 8. Audit Trail Design

**Events to log:** `login`, `login_failed`, `logout`, `mfa_challenge`, `ticket.view`, `ticket.assign`, `ticket.reassign`, `ticket.state_change`, `message.send`, `message.read`, `doc.upload`, `doc.download`, `doc.delete`, `permission.grant/revoke`, `data.export`, `dsr.request/fulfilled`, `webhook.received`, `outbound_api.call` (with cost for X pay-per-use).

**Schema (append-only, hash-chained, partitioned monthly):**

```sql
CREATE TABLE audit_events (
  seq BIGSERIAL,
  ts TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  tenant_id UUID NOT NULL,
  actor_type TEXT NOT NULL,    -- 'agent','system','customer'
  actor_id UUID,
  action TEXT NOT NULL,
  resource_type TEXT,
  resource_id TEXT,
  correlation_id UUID NOT NULL,
  payload JSONB NOT NULL,
  prev_hash BYTEA NOT NULL,
  hash BYTEA NOT NULL,         -- sha256(seq || ts || actor || action || payload || prev_hash)
  PRIMARY KEY (ts, seq)
) PARTITION BY RANGE (ts);

REVOKE UPDATE, DELETE ON audit_events FROM PUBLIC;
-- Only audit_writer role has INSERT; admins have SELECT only.
```

Hash-chaining provides tamper evidence: per AppMaster's guide, *"A defensible audit trail should be hard to change and easy to verify. Start with access control: the audit table must be append-only in practice. The application role should insert (and usually read), but not update or delete."* Plus a **periodic Merkle root** (every 1,000 events) signed with an Ed25519 key kept in Vault and **anchored externally** (e.g., emailed daily digest to the DPO inbox, or notarized via a free OpenTimestamps Bitcoin anchor). This separation-of-duties principle is what makes the audit defensible — *"If the same admin account controls both the audit log and the Merkle roots, an attacker can rewrite both and hide their tracks."*

**Correlation IDs:** Generated at the API Gateway (`X-Correlation-ID` header, ULID), propagated via OpenTelemetry baggage across every gRPC/NATS hop. Every audit row carries the same correlation ID so a customer interaction (webhook → assign → reply → close) reconstructs as one timeline.

**Retention:** Kenya DPA mandates retention only as long as necessary. Recommend **7 years for audit (financial-record analogue, common Kenyan accounting standard)**, **2 years for message content** by default with tenant-override, **30 days for document objects after ticket close** unless explicit hold.

**DPO/Supervisor search UI:** Grafana dashboard over a read-only audit view + a dedicated "Audit Search" page in the agent app limited to `auditor` and `dpo` roles.

---

## 9. Scalability & Reliability

**Message broker — pick NATS JetStream.** Rationale: per published 2025 benchmarks (Onidel), NATS JetStream sustains **200,000–400,000 msg/sec with persistence and sub-millisecond latency for in-memory, 1–5 ms with persistence**, vs RabbitMQ's 50–100k/s at 5–20 ms. Operational footprint is tiny ("**6 MiB of RAM on cold start**" vs Kafka's 327 MiB per Petrolmuffin's brokers benchmark); single Go binary, no ZooKeeper, no Erlang VM. For 100 agents × hundreds of thousands of messages/day you are nowhere near Kafka territory. Kafka becomes justified only at firehose-style event streaming (>1M msg/s) or when downstream analytics demand replay across weeks; NATS JetStream supports replay too.

**Redis:** session denylist, rate-limit counters (token-bucket via `redis_rate`), WebSocket pub/sub fan-out, agent presence, hot ticket cache (5-min TTL).

**Database scaling path:**
1. Single Postgres 16 + **pgbouncer** (transaction pooling, ~5k connections fanned to 100 PG backends).
2. Add **read replica** for analytics + audit search.
3. **Partition** `messages` and `audit_events` monthly; drop old partitions cheaply.
4. **Logical sharding** by `tenant_id` only when one tenant grows past ~50 GB.
5. Consider **Citus** or move audit to TimescaleDB only if sustained ingest passes ~10k events/s.

**Horizontal scaling:** API gateway, Ticket, Auth, Doc, Routing, Realtime are all stateless and scale by replica count. Connectors are stateless but each platform's webhook URL must point at a load-balanced endpoint; Traefik handles this.

**Resilience patterns:**
- **Circuit breaker** (sony/gobreaker) wrapping every Graph API / X API call: open after 5 consecutive failures or 50% error rate, half-open after 30s.
- **Exponential backoff with jitter** for retries (backoff/v5, base 500ms, max 5min, full jitter).
- **Idempotency keys** on every outbound API call (`Idempotency-Key: <ticket_id>:<message_id>`) and every accepted webhook (dedupe by platform message ID in Redis SET with 24h TTL).
- **Dead-letter** stream `dlq.<service>` for poison events; manual replay UI in admin.
- **Graceful degradation:** If FB connector is down, the agent UI shows "FB temporarily unavailable" but other channels keep working — strictly enforced via the bounded-context boundaries.

**WebSocket scaling:** **No sticky sessions** required if you adopt the shared-state pattern (per WebSocket.org's scaling guide: "Instead of relying on clients always reaching the same server, store connection and session state externally (Redis, a database, or another shared store). This way, any server can handle any reconnecting client"). Each WS instance subscribes to Redis channels for the agents it currently holds; when Ticket Svc emits `agent:42:new_message`, all WS instances receive it via Redis pub/sub but only the one holding agent 42's socket forwards it. A single Go process tuned (`ulimit -n 1048576`, `GOGC=200`) handles 100k idle WS connections on a 4 vCPU/8GB node.

**Load testing:** **k6** for scenarios (login, send message, document upload), **vegeta** for raw HTTP saturation, **NATS bench** for broker. Target SLOs: p95 reply send < 300 ms, webhook ingest p95 < 150 ms, presence update < 50 ms.

---

## 10. Real-Time Communication (Agent UI ↔ Backend)

- **WebSocket** is primary: `wss://app/ws/agent` with JWT in subprotocol header (`Sec-WebSocket-Protocol: bearer.<jwt>`).
- **Channels** (Redis pub/sub keys): `ws:agent:{id}` (per-agent), `ws:ticket:{id}` (per-ticket subscribers), `ws:tenant:{id}:supervisor` (supervisor broadcasts).
- **SSE fallback** at `/sse/agent` for corporate networks that block WS upgrade; same payload format, one-way.
- **Heartbeat** ping/pong every 15s; reconnect with exponential backoff on the client.
- **Message envelope:** `{type, ts, correlation_id, payload}`; types include `ticket.assigned`, `message.new`, `presence.update`, `sla.warning`, `system.notice`.

---

## 11. Deployment Plan

### Phase 1 — Single VPS, Docker Compose

```yaml
# docker-compose.prod.yml  (excerpt)
services:
  traefik:
    image: traefik:v3.2
    command:
      - --providers.docker=true
      - --providers.docker.exposedbydefault=false
      - --entrypoints.websecure.address=:443
      - --certificatesresolvers.le.acme.email=ops@example.co.ke
      - --certificatesresolvers.le.acme.storage=/acme.json
      - --certificatesresolvers.le.acme.tlschallenge=true
    ports: ["80:80","443:443"]
    volumes: ["/var/run/docker.sock:/var/run/docker.sock:ro","./acme.json:/acme.json"]
    networks: [edge]

  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_pw
    secrets: [pg_pw]
    volumes: ["pgdata:/var/lib/postgresql/data","./pg/postgresql.conf:/etc/postgresql/postgresql.conf:ro"]
    command: ["postgres","-c","config_file=/etc/postgresql/postgresql.conf"]
    networks: [data]

  pgbouncer:
    image: edoburu/pgbouncer
    environment: { DB_HOST: postgres, POOL_MODE: transaction, MAX_CLIENT_CONN: 5000 }
    networks: [data]

  redis: { image: redis:7-alpine, command: ["redis-server","--save","60","1"], networks: [data] }

  nats:
    image: nats:2.10-alpine
    command: ["-js","-sd","/data"]
    volumes: ["natsdata:/data"]
    networks: [data]

  minio:
    image: minio/minio
    command: server /data --console-address :9001
    environment: { MINIO_ROOT_USER_FILE: /run/secrets/minio_user, MINIO_ROOT_PASSWORD_FILE: /run/secrets/minio_pw }
    secrets: [minio_user, minio_pw]
    volumes: ["miniodata:/data"]
    networks: [data]

  clamav: { image: clamav/clamav:latest, networks: [data], volumes: ["clamavdb:/var/lib/clamav"] }

  zitadel:
    image: ghcr.io/zitadel/zitadel:latest
    command: ["start-from-init","--masterkey","${ZITADEL_MASTERKEY}","--tlsMode","external"]
    depends_on: [postgres]
    networks: [data, edge]
    labels:
      - traefik.enable=true
      - traefik.http.routers.iam.rule=Host(`auth.example.co.ke`)
      - traefik.http.routers.iam.tls.certresolver=le

  gateway: { build: ./services/gateway, networks: [edge, data], labels: [...] }
  ticket-svc: { build: ./services/ticket, networks: [data] }
  doc-svc: { build: ./services/doc, networks: [data] }
  fb-connector: { build: ./services/fb, networks: [data, edge], labels: [...] }
  x-connector: { build: ./services/x, networks: [data, edge], labels: [...] }
  ws-svc: { build: ./services/ws, networks: [edge, data], labels: [...] }
  audit-svc: { build: ./services/audit, networks: [data] }

networks: { edge: {}, data: { internal: true } }
volumes: { pgdata: {}, miniodata: {}, natsdata: {}, clamavdb: {} }
secrets:
  pg_pw: { file: ./secrets/pg_pw }
  minio_user: { file: ./secrets/minio_user }
  minio_pw: { file: ./secrets/minio_pw }
```

**Secrets:** Docker secrets for Phase 1; **SOPS + age** for the git repo (encrypts `secrets/*` per-environment). Vault when moving to K8s.

**Backup:**
- `pg_basebackup` weekly + **WAL archiving** to a separate object bucket every 5 min (PITR window: 7 days).
- MinIO **versioning + lifecycle** + nightly `mc mirror` to off-site bucket (preferably Kenya-region; see §13 on data localisation).
- Encrypted backups; restore drill quarterly.

**VPS sizing for Phase 1 (100 agents target):** 8 vCPU / 32 GB RAM / 500 GB NVMe. Postgres + MinIO + ClamAV are the heavy ones.

### Phase 2 — Kubernetes (K3s → full K8s)

Migrate when: (a) one box can't fit the working set, (b) you need ≥3 nines availability, (c) connector traffic grows past ~50 req/s sustained per connector. **K3s** on 3 nodes is the practical step from Compose: single binary, embedded etcd alternative, Helm-compatible.

Changes:
- Docker Compose → Helm charts (one per service).
- Docker secrets → **External Secrets Operator** backed by Vault.
- Traefik → Traefik Ingress (or replace with NGINX Ingress if your team prefers).
- Postgres → managed (e.g., Crunchy Data Operator) or **CloudNativePG**.
- MinIO → MinIO Operator (multi-node erasure coded).
- NATS → official Helm chart, 3-node cluster.
- Add **Horizontal Pod Autoscaler** on CPU + custom metric (`ws_connections`).

### CI/CD

GitHub Actions (or GitLab CI) pipeline per service:
1. `go vet`, `staticcheck`, `golangci-lint`.
2. `go test -race -cover` (≥80% coverage gate on critical paths).
3. `nancy` / `govulncheck` for Go module CVEs.
4. Build distroless multi-stage image: `FROM gcr.io/distroless/static-debian12:nonroot`, USER 65532, no shell.
5. **Trivy** image scan (block on HIGH/CRITICAL).
6. **Cosign** sign image; push to private registry.
7. ArgoCD (Phase 2) or `docker compose pull && up -d` via SSH (Phase 1).

---

## 12. Observability

| Layer | Tool | Notes |
|---|---|---|
| Logs | **slog (JSON)** → Promtail → **Loki** | Per-service labels: `service`, `tenant_id`, `correlation_id`. PII-redaction middleware. |
| Metrics | **Prometheus** scraping `/metrics` (OTel-exposed) + Grafana | Golden signals + per-channel ingest rate + X API spend (custom counter). |
| Traces | **OpenTelemetry SDK** → OTLP → **Tempo** | Sampling: head 10%, plus 100% for error spans and webhook ingest. |
| Alerts | **Alertmanager** → email + on-call (Opsgenie or self-hosted **Karma** + ntfy.sh) | SLO-based: burn-rate alerts on p95 latency, error budget. |
| Health | `/healthz` (liveness, lightweight), `/readyz` (deps), `/startupz` | Connectors expose `webhook_signature_failure_total` — page on > 0 in 5 min. |

---

## 13. Kenya Data Protection Act 2019 — Compliance Checklist

| Item | Action |
|---|---|
| **ODPC registration** | Register as **Data Controller** (you control agents'/employees' data) and **Data Processor** (you process brands' customers' data on their behalf). Fee **KES 4,000**, renewal **KES 2,000**, certificate issued within 14 days, valid **24 months**. Use https://www.odpc.go.ke. Exemption only if turnover < KES 5M and < 10 employees AND not in a mandatory industry (you won't qualify). |
| **Lawful basis** | Document under DPA Section 30: (a) **Contract** with brand for processing their customers' data; (b) **Legitimate interest** for service operation logs; (c) **Consent** for any optional analytics. Sensitive data (e.g., national ID images shared in tickets) requires **explicit consent**. |
| **Data subject rights** (Sections 26 + 40) | Build a DSR module: access, rectification, erasure, restriction, portability, objection. Respond **within 30 days** (statutory). Erasure flow must cascade to tickets, messages, documents (MinIO delete + audit). |
| **Cross-border transfer** (Section 48) | Kenya allows transfers if (a) adequacy decision, (b) appropriate safeguards, (c) explicit consent, or (d) necessity. Per the EU's EEAS statement, on **7 May 2024, Nairobi**, the EU and Kenya announced "the first Adequacy Dialogue on the African continent" — not yet concluded; treat EU as not-yet-adequate. Since FB/X/WA store data in the US, you must document this transfer in the privacy notice and DPIA, get explicit consent or rely on standard contractual clauses with Meta/X (their DPAs cover this). The **Cloud Policy (Dec 2024)** encourages localisation of sensitive data — keep PG/MinIO primary copies in Kenya, replicate only encrypted backups abroad. |
| **Breach notification** (Section 43) | Notify the Data Commissioner via the ODPC online breach portal **within 72 hours** of awareness. Processor → controller within **48 h**. Notify affected data subjects "without undue delay" if high risk. Build templates and a runbook now; the form requires breach circumstances, chronological account, assessment, mitigations, lessons learned. Failure to report can result in fines of **up to KES 5,000,000 or 1% of annual turnover, whichever is lower** — the pending **Data Protection (Amendment) Bill 2025** proposes flipping this to "whichever is higher." |
| **DPIA** (Section 31) | Mandatory because you process large-scale data, monitor data subjects systematically (read messages), and process potentially sensitive data. Submit **60 days before processing**; consult ODPC if residual risk is high. |
| **DPO appointment** | Mandatory: you process sensitive data at scale and your core activity is monitoring. Appoint a DPO (can be outsourced), publish contact details on website. |
| **Privacy notice & consent** | Public privacy notice on widget and signup; granular consent capture. Use the ODPC Guidance Note on Consent. |
| **Records of processing** | Although DPA doesn't explicitly mandate ROPA, ODPC audits expect one; maintain a Salesforce-style register. |
| **Pending changes to watch** | **Data Protection (Amendment) Bill, 2025** — expands sensitive data (political opinions, trade union membership), adds Data Protection Appeals Tribunal (Sections 64A–64F), broadens complainant standing to "any person" (not just data subjects), and **proposes higher penalties** (flipping "lower" to "higher"). **Draft Compliance Audit Regulations, 2024** and **Data Sharing Code (2024)** in consultation. Track ODPC determinations (https://www.odpc.go.ke/determinations/) — January 2025 ruling shows enforcement is active. |

**Concrete Kenyan-specific deployment notes:**
- Host primary Postgres and MinIO in a Kenya-region data centre (Liquid/CSquared, Africa Data Centres, iColo) per the December 2024 Cloud Policy localisation guidance.
- Reference Section 25 (security measures) implementation in your DPIA: encryption at rest/transit, MFA, audit logs, AV, regular penetration tests.

---

## 14. Voice/Call Integration — Future Architecture

Add a **Voice Connector** service later that fronts one of:

- **Africa's Talking Voice API** — Kenyan-incorporated, supports local DIDs, USSD, SIP termination; lowest friction for a Kenyan deployment, and likely cheaper for local minutes.
- **Twilio Programmable Voice** — globally consistent, richer SDKs, more expensive.
- **FreeSWITCH / Asterisk** self-hosted — full control, requires telecom expertise; SIP trunk from Safaricom Business or AccessKenya.
- **WebRTC softphone** in the agent UI (jssip + a media server like FreeSWITCH or Janus) — pairs well with any of the above.

Pattern: Voice Connector receives call events (ringing, answered, hangup) and **creates a Ticket of channel='voice'** in the same data model. Recording stored as a document via Doc Svc (encrypted; consent captured by an IVR prompt — Kenya DPA requires explicit consent for recording). DTMF transfers and supervisor barge-in via a presence-aware extension to the Routing engine.

---

## 15. Self-Hosted Support Page Widget

Embeddable JS:

```html
<script src="https://cdn.example.co.ke/widget.js"
        data-tenant="acme-ke" data-channel="widget" async></script>
```

The widget opens a **WebSocket** to `wss://app/ws/widget`, identifies anonymously (browser-fingerprint + optional email/phone), and creates a `conversation` with `channel='widget'`. Backed by Widget Connector → same `ingress.widget.*` event → same Ticket Service path → same agent UI. File uploads go through Doc Svc with pre-upload AV. Identity escalates to a real customer record when the user provides email/phone (DPA: consent banner before collection).

---

## 16. Phased Roadmap (Small Team: 1 PM, 2-3 Go engineers, 1 frontend, 0.5 DevOps)

| Phase | Scope | Duration |
|---|---|---|
| **Phase 0 — Foundations** | Repo + monorepo layout, CI/CD, Compose stack (PG, Redis, NATS, MinIO, Zitadel, Traefik), Auth+Agent management, Ticket Svc + open/closed lifecycle, Audit Svc with hash chain, **Facebook connector** (Messenger DMs + webhook), basic agent UI. ODPC registration started in parallel. | **~10–12 weeks** |
| **Phase 1 — Multi-channel + Documents** | **X connector** (Account Activity API + reply via v2), Doc Svc with ClamAV + presigned URLs, Routing engine v1 (hybrid), SLA timers, supervisor dashboards, DPIA filed. App Review (FB + X) submitted ~week 4 of this phase. | **~10–12 weeks** |
| **Phase 2 — Scale-out channels & analytics** | WhatsApp Cloud API connector, Instagram Business connector, Analytics Svc, advanced routing (skills, priority, escalation), tenant onboarding flow, full DSR module. | **~10–14 weeks** |
| **Phase 3 — Voice, widget, platform hardening** | Self-hosted widget GA, Voice connector (Africa's Talking pilot), K3s/K8s migration, multi-region DR, SOC2-style pentest. | **~12–16 weeks** |

**Total to GA with 4 channels:** roughly **7–9 months**. Voice + K8s + widget land at **~12 months**.

---

## 17. Risks, Anti-Patterns, and Gotchas

| Risk | Mitigation |
|---|---|
| **Premature microservices** | Start as a modular monolith with strict package boundaries (`internal/auth`, `internal/ticket`, …). Only extract a service when it has a different scaling profile or a separate team owns it. The connectors and Doc Svc are the legitimate first extractions because they have isolated dependencies (Graph/X SDKs, ClamAV). |
| **Shared brand-account rate limits** | One Page token serves all agents → BUC quotas are shared. Implement a **per-Page outbound queue** (NATS work-queue with concurrency=1 per Page) to serialise replies; expose live `X-Business-Use-Case-Usage` percentages in the supervisor dashboard. |
| **X pay-per-use cost runaway** | Per Postproxy's 2026 analysis, "Pay-per-use is also capped at 2 million post reads per month. Above that threshold, Enterprise is required." Enable X's **spending caps and auto top-up** controls. Track per-tenant outbound spend in a Prom counter and alert at 70% of budget. Avoid the $0.20-per-URL-post trap — strip auto-link cards from outbound replies unless absolutely needed. |
| **Meta App Review denial loop** | Build the **screencast** demo first; demonstrate every permission in actual use. Have **Business Verification** complete before submitting. Expect 4–8 weeks. `pages_read_user_content` denials are common — phrase justifications around "support agent reads inbound customer comments to reply." |
| **Webhook duplicates / replay** | Dedupe by `(platform, platform_event_id)` in Redis SET with 24h TTL. Process idempotently. X AAA explicitly warns: "If your app has subscriptions for User A and User B, and User A mentions User B in a Post, your webhook receives two events." Use `for_user_id` to disambiguate. |
| **Webhook signature edge case** | Always verify against the **raw** body. Don't trust your JSON parser to preserve byte equivalence. Reject with 401, never 500 (Meta interprets 5xx as transient and retries). |
| **Kenya DPA storing third-party message content** | Brands are joint/data controllers for their customers' messages flowing through your system. Contract this explicitly in your DPA-with-tenant. Implement per-tenant retention controls; default 24 months for messages, configurable down to 30 days. |
| **Token leakage / Page token theft** | Store Page tokens encrypted (Vault transit secret engine; envelope per-tenant DEK). Never log tokens. Rotate on any incident. Page tokens "do not have an expiration date" — that means theft is permanent until rotated. |
| **Single point of failure on VPS phase** | Document the K3s migration trigger now (CPU > 70% sustained, or DB > 60% utilization, or > 50 concurrent agents). Practice a same-region restore-from-backup drill *before* you actually need it. |
| **GORM in the hot path** | Don't. Per Phil Eaton's October 2023 PG insert benchmark, even the raw lib/pq driver runs at ~44–76% of pgx native throughput; adding GORM's reflection on top compounds the cost. Use sqlc+pgx for messages, audit, routing queries. |
| **WebSockets with sticky sessions on Compose** | Implement the Redis pub/sub fan-out from day one — Traefik in Compose doesn't do sticky sessions cleanly, and you'll need it anyway in K8s. |
| **Voice consent missed** | Kenya DPA requires explicit consent to record. Build an IVR prompt: "This call may be recorded for support — press 1 to consent, 2 to opt out." Store the consent event in audit. |
| **PII in logs** | slog middleware that scrubs national IDs (8-digit Kenyan ID pattern), phone numbers, document tokens. Pen-test asks for this. |

---

## Recommendations (Staged, Actionable Next Steps)

**Week 1–2 (decision gates):**
1. Pick **monolith vs services** explicitly. Default: monolith with package boundaries. Revisit when team > 6 engineers OR a service hits CPU > 70% sustained.
2. Start ODPC registration paperwork; appoint a DPO (interim if outsourced).
3. Spin up a developer Meta App and X developer account; submit Business Verification immediately because it blocks everything else.

**Week 3–6:** Stand up Phase 0 Compose stack on a staging VPS; implement Facebook connector end-to-end with one test brand Page; ship `audit_events` hash chain; ship Ticket Svc + Zitadel agent login.

**Threshold to extract first service:** Connector CPU > 50% sustained, OR ticket DB write latency p95 > 50ms, OR > 30 concurrent agents on staging. At that point, extract FB and X connectors to their own processes first (they're already cleanly bounded).

**Threshold to migrate to K3s:** Single VPS CPU > 70% sustained, OR you need >99.5% availability (one VPS can't deliver that), OR more than ~50 concurrent agents, OR you onboard a second large tenant (e.g., another @ecitizen-scale brand). Plan the migration to take **3–4 weeks** of dedicated DevOps work.

**Threshold to negotiate X Enterprise:** Monthly read consumption > 1.5M (75% of the 2M cap) sustained. Below that, stay on pay-per-use with hard spend caps.

**Threshold to add WhatsApp:** A signed contract worth more than ~12 months of WA template costs (per Meta's July 2025 per-message model, marketing templates in Kenya run roughly $0.025–0.04 each — material at scale). WhatsApp adds template-approval ops burden that a small team should not take on speculatively.

---

## Caveats

- **X API pricing has changed 4 times since 2023 and twice in 2026 alone** (February pay-per-use launch; April 20 write-price increase and Enterprise-only restrictions for some actions). The numbers cited here are accurate as of May 2026 but assume they may shift again — keep cost-tracking telemetry permanent.
- **Meta App Review timelines are variable** — Meta's published "up to 10 business days" is best-case; community reports describe 2–4 cycle attempts as typical for complex multi-permission requests. Budget contingency.
- **Kenya Data Protection (Amendment) Bill 2025 has not been enacted as of May 2026**; the higher-penalty regime and Appeals Tribunal are proposed but not yet law. Track parliamentary progress.
- The **Kenya–EU adequacy dialogue** announced 7 May 2024 has not concluded as of May 2026; do not architect on the assumption of EU adequacy.
- **Closed-form rate limits for Meta `pages` and `messenger` Business Use Cases are not publicly documented**; this plan relies on the BUC header (`X-Business-Use-Case-Usage`) for real-time observation rather than hard-coded quotas.
- **Self-hosted IdP comparisons** reflect current 2025–2026 community opinion; Keycloak remains the largest community by far and is a defensible alternative if your team has Java operations expertise — Zitadel's primary advantage for *this* project is being Go-native and lighter to operate.
- WhatsApp Business pricing transitioned from conversation-based to **per-message billing on July 1, 2025**, with further rate changes through 2026. Service messages within the 24-hour customer window remain free, but template costs vary substantially by country and category — model your unit economics with current rate cards before launch.

---

## Completion Coverage

| Deliverable | Section | Covered |
|---|---|---|
| 1. Architecture diagram | §1 | ✅ |
| 2. Microservice breakdown | §2 | ✅ |
| 3. Go stack | §3 | ✅ |
| 4. Social API integration | §4 | ✅ |
| 5. AuthN/AuthZ | §5 | ✅ |
| 6. Ticket lifecycle & routing | §6 | ✅ |
| 7. Document security | §7 | ✅ |
| 8. Audit trail | §8 | ✅ |
| 9. Scalability | §9 | ✅ |
| 10. Real-time | §10 | ✅ |
| 11. Deployment | §11 | ✅ |
| 12. Observability | §12 | ✅ |
| 13. Kenya DPA compliance | §13 | ✅ |
| 14. Voice future | §14 | ✅ |
| 15. Self-hosted widget | §15 | ✅ |
| 16. Phased roadmap | §16 | ✅ |
| 17. Risks/gotchas | §17 | ✅ |

For a Kenya-based, multi-tenant social-media contact-center sold to brands like @ecitizen and built by a small Go team, the pragmatic path is: **a modular monolith with sharply defined service-boundary packages, deployed on Docker Compose with NATS JetStream + Postgres + Redis + MinIO + Zitadel + Traefik on a single beefy VPS**, registered with the ODPC under the Data Protection Act 2019, and architected so that the four hot extractions (FB connector, X connector, Doc Svc, Realtime WS) can lift out to K3s once concrete scale thresholds are crossed.

The **two highest-risk dependencies are platform-side**: Meta App Review (4–8 weeks, plan early) and X's February 2026 pay-per-use pricing (build the X connector with cost telemetry from day one). The **highest-risk internal mistake** would be premature service splitting before the team is operationally ready — resist that, and the rest is execution.