# Demo runbook

Get from a fresh checkout to "I just messaged my Facebook Page from
my phone and the message appears in the agent inbox" in ~30 minutes.

## What you'll see

```
                      ┌──────────────────────────────────┐
   You on FB ───┐     │  cloudflared (HTTPS public URL)  │
                ├────▶│  http://localhost:8080  gateway  │
   Visitor ─────┤     │  http://localhost:8081  ws       │
   on demo HTML │     │  http://localhost:3000  agent UI │
                │     └──────────────────────────────────┘
                │            │
                ▼            ▼
        widget WS     ingress.>  (NATS JetStream)
                            │
                            ▼
              Ticket Service consumer  →  PG: customers/conversations/tickets/messages
                            │
                            ▼
                Agent UI inbox  ◀────── /v1/tickets list, refreshed by /ws/agent push
```

## Prereqs

- Linux/macOS with Docker, `docker compose`, `make`, `psql`, Go 1.25+, Node 22+, Python 3 (for the static-file server).
- A Meta Developer account at <https://developers.facebook.com>.
- A Facebook Page you own (or a Test Page you create — Meta's
  Business Settings → Pages → Add → "Create a new test Page").
- The free `cloudflared` Docker image (auto-pulled by `make demo-up`).

## 1. Bring the stack up

```sh
make demo                # builds images, brings everything up, runs migrations + seed
```

When it finishes you'll see the click-through summary. Verify:

```sh
curl -sf http://localhost:8080/healthz       # → ok
curl -sf http://localhost:8080/readyz        # → ready
```

## 2. Start the agent UI

In a second terminal:

```sh
cd web/agent && npm install                  # first run only
make demo-ui                                 # http://localhost:3000
```

Sign in at `http://localhost:3000` with `ada@demo.local` (any
password). You should see 5 demo tickets across 4 channels in the
inbox. Other seeded logins:

| Email | Role | What changes in the UI |
|---|---|---|
| `ada@demo.local`   | agent      | inbox of tickets assigned to her |
| `bob@demo.local`   | supervisor | adds the queue + agents + at-risk dashboards |
| `carol@demo.local` | admin      | adds onboarding + tenant retention |
| `diana@demo.local` | dpo        | adds the DSR queue |

To exercise every endpoint directly (without the agent UI), import
`docs/demo/postman/{collection,environment}.json` — see
`docs/demo/postman/README.md` for the walkthrough. Each role has its
own pre-baked bearer; the `10. Negative — RBAC denies` folder proves
the gates work.

## 3. Try the widget (no Facebook needed)

In a third terminal:

```sh
make demo-widget                             # serves web/widget/ on :8000
```

Open `http://localhost:8000/demo.html` → click 💬 → type a message.
Within ~1 second a ticket appears in the agent UI inbox.

### Self-register a new widget site

The seed pre-registers `http://localhost:8000` so the bundled demo
page works out of the box. To register a brand's real site:

1. Sign in to the agent UI as `carol@demo.local` (admin).
2. Click **Widgets** in the left nav.
3. Fill in **Origin** (`https://www.acme.co.ke`), **Display name**,
   optional **Welcome message** → **Register site**.
4. The new row appears with a freshly-generated `embed_key`. Click
   **Show snippet** → **Copy** to grab the `<script>` tag.
5. Paste it just before `</body>` on the brand site.

The gateway's `/ws/widget` upgrade checks the visitor's `Origin`
header against the registered list — only origins on the table can
open a socket, so the snippet can't be stolen and reused on a
different domain.

Remove a site with the **Remove** button; the embed_key is invalidated
immediately. Same origin can be re-registered any time (new embed_key
each time).

## 4. Wire up Facebook (real DMs)

This is the substantive part. Two pieces need to line up: a Meta
Developer App, and the cloudflared URL Meta will POST webhooks to.

### 4a. Get the public URL Meta will hit

```sh
make demo-tunnel
# → https://something-something.trycloudflare.com
```

Save it — you'll paste it into Meta in 4d below.

### 4b. Create a Meta Developer App

1. <https://developers.facebook.com/apps> → **Create App**
2. Pick "**Business**" type.
3. App name: `contactcentre-demo` (or anything).
4. Once created, in the left nav: **Add a Product** → **Messenger** → Set Up.
5. Also add: **Webhooks** → Set Up.

### 4c. Copy your App credentials into the gateway

Edit `deploy/docker-compose.demo.yml` → `gateway` service →
`environment:`, add three lines:

```yaml
      FB_APP_ID:        "<your App ID from the Meta dashboard>"
      FB_APP_SECRET:    "<your App Secret — Settings → Basic → App Secret>"
      FB_REDIRECT_URI:  "https://<your-tunnel-url>/v1/connect/fb/callback"
```

Replace `<your-tunnel-url>` with the cloudflared URL from 4a.

Restart the gateway so it picks up the env vars:

```sh
docker compose -f deploy/docker-compose.demo.yml up -d gateway
```

### 4d. Configure Meta App: Messenger + Instagram + WhatsApp webhooks

Back in the Meta Developer dashboard, **add three Products** to the
App: **Messenger**, **Instagram**, and **WhatsApp**. One App = one set
of credentials covering all three channels.

**Messenger → Settings → Webhooks → Configure:**

- Callback URL: `https://<your-tunnel-url>/v1/fb/webhook`
- Verify Token: paste your `FB_APP_SECRET` (the demo wires verify
  token = app secret for simplicity; production uses a separate value)
- Subscription fields: `messages`, `messaging_postbacks`,
  `message_deliveries`, `message_reads`, `feed`, `mention`

**Instagram → Webhooks → Configure:**

- Callback URL: `https://<your-tunnel-url>/v1/ig/webhook`
- Verify Token: same `FB_APP_SECRET`
- Subscription fields: `messages`, `comments`, `mentions`

**WhatsApp → Configuration → Webhooks → Edit:**

- Callback URL: `https://<your-tunnel-url>/v1/wa/webhook`
- Verify Token: same `FB_APP_SECRET`
- Subscribe to fields: `messages`, `message_status_updates`

Click **Verify and Save** on each. Meta will GET each URL with the
verify token; the gateway echoes the challenge back.

**App Settings → Basic → Add Platform → Website:**

- Site URL: `https://<your-tunnel-url>/`

**Facebook Login → Settings → Valid OAuth Redirect URIs:**

- `https://<your-tunnel-url>/v1/connect/fb/callback`

### 4e. Add yourself as a test user (development mode)

Until App Review (4-8 weeks), only **App Roles** can interact with
the app. **App Roles → Add People** → add your own Facebook account
as **Tester**.

Accept the invite from your personal Facebook notifications.

### 4f. Connect everything (one click)

In the agent UI, sign in as `bob@demo.local` (supervisor) →
**Channels** → **Connect with Facebook**.

You'll be redirected to Meta's consent screen. Pick the Page(s) you
want to connect (and tick the WhatsApp + Instagram permissions if Meta
prompts). After consent, the gateway:

1. Exchanges the code for a long-lived user token.
2. Lists every Page you admin and subscribes each to webhook fields →
   stores in `fb_pages`.
3. For every connected Page, looks up its linked Instagram Business
   Account (`/{page_id}?fields=instagram_business_account`) and stores
   it in `ig_accounts` (Path 1, FK to fb_page_id).
4. Walks `/me/businesses` → owned WABAs → phone numbers, subscribes
   each WABA, stores each phone number in `wa_phone_numbers`.

You'll land on a success page listing all three: connected Pages,
Instagram accounts, and WhatsApp numbers.

### 4g. Send yourself a message on any of the three channels

Pick whichever channel you want to demo:

| Channel | How to send | What you'll see |
|---|---|---|
| Messenger DM to your Page | Open Messenger as a different account, message the Page | Ticket appears in the agent inbox under channel `fb` |
| Instagram DM to your IG Business | Use a different IG account, DM the connected IG | Ticket appears under channel `ig` |
| WhatsApp message to your WA Business number | Send a WhatsApp message to your verified WA number from another phone | Ticket appears under channel `wa` |
| Comment on a Page post | Comment on any post from a different account | Ticket appears under channel `fb`, kind `postback` |

In every case the webhook fires →
`ingress.{fb|ig|wa}.message` (or `comment`) → ticket consumer creates
a customer + conversation + ticket + message → agent UI inbox refreshes
in ~1 second.

## 5. Tear down

```sh
make demo-down                               # stops + removes containers + volumes
```

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| "tunnel URL not found in cloudflared logs after 2 minutes" | cloudflared still warming up | re-run `make demo-tunnel` |
| Meta webhook config fails verification | `FB_APP_SECRET` mismatch | confirm verify-token field matches the env var |
| OAuth redirects to "URL Blocked" | Redirect URI missing in Facebook Login settings | add it under **Facebook Login → Settings → Valid OAuth Redirect URIs** |
| OAuth completes but "no Pages associated" | The FB account isn't a Page admin | use a personal account that admins at least one Page |
| DM sent but no ticket appears | tunnel rotated since you saved the webhook | re-run `make demo-tunnel`, update Meta's webhook URL |
| Agent UI shows empty inbox after Facebook DM | role mismatch — Ada has agent role only sees mine=1 | sign in as `bob@demo.local` (supervisor) for full tenant view |

## Realtime inbox refresh

The agent UI subscribes to `/ws/agent` once per session (see
`web/agent/components/RealtimeRefresher.tsx`) and calls
`router.refresh()` on every inbound WS frame. The gateway publishes
those frames when:

| Trigger | Envelope `type` | Channels published to |
|---|---|---|
| Inbound message lands (any connector) | `message.new.inbound` | `SupervisorChannel(tenant)` + `AgentChannel(assignee)` if any |
| Agent reply posted (`POST /v1/tickets/.../messages`) | `message.new.outbound` | `SupervisorChannel` + `AgentChannel` (assignee) |
| Ticket state changes (`PATCH /v1/tickets/.../state`) | `ticket.state_change` | `SupervisorChannel` + `AgentChannel` (assignee) |

So a regular agent (Ada) only sees activity on tickets assigned to her;
a supervisor (Bob) sees the whole tenant. Verified end-to-end against
both bearer roles: state change → frame in ~50ms; inbound NATS event →
frame in ~1.3s (mostly the ingress consumer pull cadence).

Set `REDIS_ADDR=""` on the gateway to disable (workers + API still
work, just no auto-refresh — the agent has to manually reload).

## What outbound looks like now

Four NATS consumers split the `outbound.>` workqueue, three real and
one stub:

| Subject | Consumer | Action |
|---|---|---|
| `outbound.fb.text` | `fb-outbound` | `PGPageStore` decrypt → POST `graph.facebook.com/<ver>/<page>/messages` → `sent_fb` / `failed_fb` / `no_route` in `outbound_log` |
| `outbound.ig.text` | `ig-outbound` | `PGTokenStore` decrypt (Path 1 rides the linked Page) → POST `graph.facebook.com/<ver>/<ig_user>/messages` → `sent_ig` / `failed_ig` / `no_route` |
| `outbound.wa.text` | `wa-outbound` | `PGNumberStore` decrypt → POST `graph.facebook.com/<ver>/<phone_number_id>/messages` (Bearer auth, WA payload) → `sent_wa` / `failed_wa` / `no_route` |
| `outbound.{x,widget,voice}.*` | `outbound-stub` | Writes `agent_reply` to `outbound_log` — placeholder until those senders land |

Until you complete **Connect with Facebook**, all three Meta workers
log `no_route` (no rows in `fb_pages` / `ig_accounts` /
`wa_phone_numbers` → nowhere to send). After OAuth, every Postman /
agent-UI reply on an `fb` / `ig` / `wa` ticket actually calls Meta.

Watch the worker that matches the channel you're testing:

```
docker logs contactcentre-demo-fb-outbound-1
docker logs contactcentre-demo-ig-outbound-1
docker logs contactcentre-demo-wa-outbound-1
```

Look for the `shipped to Meta` line with the returned `message_id` (or
`wamid` for WhatsApp).

## What's NOT in this demo

- **X (Twitter)** — different OAuth shape, plus X removed free API
  access in 2023. Account Activity API now requires a paid plan
  ($200/mo Basic minimum, $5000/mo Pro). The connector + token vault
  exist (`internal/connector/x/`); wiring an OAuth flow lands when
  someone has a paid X dev account to test against.
- **Voice (Africa's Talking)** — connector + IVR consent prompt
  already exist (`internal/connector/voice/`); needs an AT account +
  a real phone number to demo.
- **Real outbound to Meta** — agent replies currently land in
  `outbound_log` via the `outbound-stub` worker. To actually ship
  replies back via Messenger / IG / WA, replace the stub with the
  per-channel Senders (`facebook.Sender` already exists for the FB
  side; IG + WA need ~50 LOC each that mirror it). The fan-out
  publish on `outbound.<channel>.text` is already in place.
- **WhatsApp without a verified phone** — for the WA demo to actually
  fire webhooks, the WABA needs at least one verified phone number.
  Meta provides a free **test number** under WhatsApp → Getting
  Started; use that to skip the SIM verification step.
- **App Review** — required to onboard *other* brands' Pages /
  IG accounts / WA numbers. Per the technical plan §17 this typically
  takes 4-8 weeks and requires Business Verification (legal docs +
  tax ID). Until then, only people with App Roles on your Meta App
  can interact with the demo.
