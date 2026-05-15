# Postman walkthrough

Drive every authenticated endpoint from Postman, with one folder per
RBAC role and pre-baked bearers for all four demo users.

## What you'll see

```
Collection                                 Bearer         Role
──────────────────────────────────────────  ─────────────  ──────────
0. Health & probes                          (none)         —
1. Identity (any role)                      ada/bob/carol/diana  any
2. Tickets                                  ada            agent
3. Documents                                ada            agent
4. Connectors — list connected              bob            agent+
5. Supervisor                               bob            supervisor
6. Analytics                                bob            supervisor
7. DSR                                      diana          dpo
8. Onboarding                               carol          admin
9. CSAT (public)                            (none)         —
10. Negative — RBAC denies                  varies         expects 401/403
```

## 1. Bring the demo stack up

```sh
make demo                      # builds + seeds; takes ~2 min the first time
```

The seed inserts four agents with deterministic UUIDs:

| Email | UUID | Role |
|---|---|---|
| `ada@demo.local`   | `22222222-2222-2222-2222-222222222222` | agent |
| `bob@demo.local`   | `33333333-3333-3333-3333-333333333333` | supervisor |
| `carol@demo.local` | `44444444-4444-4444-4444-444444444444` | admin |
| `diana@demo.local` | `55555555-5555-5555-5555-555555555555` | dpo |

The Postman environment hardcodes all four into the `*_bearer`
variables so every request authenticates as the right role.

## 2. Import into Postman

1. Open Postman → **File → Import**.
2. Drop in **both** files from this directory:
   - `collection.json` — the requests
   - `environment.json` — base URL + bearers + scratch variables
3. Top-right environment selector → pick **`Contact Centre — Demo`**.

## 3. Run requests in order

Each folder is numbered; do them top-down the first time:

1. **`0. Health`** — confirms the gateway is reachable.
2. **`1. Identity`** — `GET /v1/me` for each role. The response shows
   `agent_id`, `tenant_id`, `roles`. The "no bearer → 401" entry
   confirms auth is wired.
3. **`2. Tickets > List`** — runs a test script that grabs the first
   row and saves `ticket_id` + `conversation_id` into the environment
   so the next requests work without copy-paste.
4. **`2. Tickets > Append message`** — sends a reply on that ticket.
   Watch the gateway logs (`make demo-logs`) and you'll see
   `outbound.<channel>.text` published; the `outbound-stub` worker
   writes it to the `outbound_log` table.
5. **`5. Supervisor`** + **`6. Analytics`** — read-only dashboards.
   Both folders pass since Bob has supervisor role.
6. **`7. DSR > Open`** — creates a Subject Access Request, saves the
   `dsr_id`. Then run **Get / Assign / Record action / Resolve** in
   that order to walk the full lifecycle.
7. **`8. Onboarding`** — Carol fetches her tenant, updates the
   retention policy, invites a new agent.
8. **`10. Negative`** — proof the RBAC gates work. Each request
   should return **403** (or **401** for the garbage bearer one).

## 4. What each role can / can't reach

| Endpoint | agent (Ada) | supervisor (Bob) | admin (Carol) | dpo (Diana) |
|---|---|---|---|---|
| `/v1/me` | ✓ | ✓ | ✓ | ✓ |
| `/v1/tickets/*` | ✓ | ✓ | ✓ | ✓ |
| `/v1/documents/*` | ✓ | ✓ | ✓ | ✓ |
| `/v1/connect/fb/*` (list) | ✓ | ✓ | ✓ | ✓ |
| `/v1/supervisor/*` | ✗ 403 | ✓ | ✓ | ✓ |
| `/v1/analytics/*` | ✗ 403 | ✓ | ✓ | ✓ |
| `/v1/dsr` (read) | ✗ 403 | ✗ 403 | ✓ | ✓ |
| `/v1/dsr/{id}/resolve` | ✗ 403 | ✗ 403 | ✓ | ✓ |
| `/v1/onboarding/*` | ✗ 403 | ✗ 403 | ✓ | ✗ 403 |

(Auditor role is also valid in code but not seeded; bearer would be
`demo:<any-uuid>:11111111-1111-1111-1111-111111111111:auditor`.)

## 5. Variables that auto-fill

| Variable | Filled by |
|---|---|
| `ticket_id` | `2. Tickets > List` test script (first row) |
| `conversation_id` | `2. Tickets > List` test script (first row) |
| `dsr_id` | `7. DSR > Open` test script (response body) |

Set manually:

| Variable | When |
|---|---|
| `document_id` | After running `3. Documents > Upload` (copy from response) |
| `csat_token` | When you have a survey token to test the public collector |

## 6. Pointing at the cloudflared tunnel

If you want to test from outside your laptop (e.g. curl from your
phone), change the `base_url` value in the environment to your
cloudflared URL from `make demo-tunnel`. Everything else still works.

## 7. Common failures and what they mean

| Status | Likely cause |
|---|---|
| 401 on a `/v1/...` request | Bearer missing or malformed. Check the environment is selected. |
| 403 on `/v1/supervisor/queue` as Ada | Working as designed — Ada is `agent`, not supervisor. |
| 500 on `/v1/tickets` | Migrations not applied or seed not run. `make demo-migrate && make demo-seed`. |
| Connection refused | Stack isn't up. `make demo` then retry. |
| 400 on DSR Open | Missing one of `subject_email`, `subject_phone`, `subject_external_ref`. The repo requires a way to identify the data subject. |
