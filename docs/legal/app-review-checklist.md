# Meta App Review submission checklist

Everything you need to fill in or upload to graduate from
"Development Mode" (only App Roles can use the app) to a Live app
that any of your brand's customers can interact with.

Typical timeline: **4–8 weeks** between submission and approval,
including back-and-forth with Meta reviewers. Plan accordingly.

---

## A. App Settings → Basic

| Field | What to enter | Source in this repo |
|---|---|---|
| **Display Name** | The brand's customer-facing name (e.g. "Acme Customer Care") | — |
| **App Icon** | 1024×1024 PNG, square, no transparency | Provide separately — not in repo |
| **Privacy Policy URL** | Public HTTPS URL of your privacy policy | Template: `docs/legal/privacy-policy.md` |
| **Terms of Service URL** | Public HTTPS URL of your terms | Provide separately |
| **Data Deletion Instructions URL** | Public HTTPS URL with deletion instructions | Template: `docs/legal/data-deletion-instructions.md` |
| **Category** | Business and Pages | — |
| **App Domains** | Your gateway's public domain (e.g. `app.acme.co.ke`) | — |

## B. App Settings → Advanced → Data Deletion Callback URL

```
https://<your-public-base-url>/v1/meta/data-deletion
```

Verify the URL by visiting your status page format:

```
https://<your-public-base-url>/v1/meta/deletion-status/00000000-0000-0000-0000-000000000000
```

It should return an HTML page (the "no data was held" variant). If
you get a 502 / connection refused, your tunnel or DNS isn't routing
yet.

The handler (`internal/meta/data_deletion.go`) verifies Meta's
HMAC-SHA256 signed_request using `FB_APP_SECRET`. **The signature
verification IS the security boundary — no bearer, no IP allowlist.**

## C. App Settings → Advanced → Deauthorize Callback URL

```
https://<your-public-base-url>/v1/meta/deauthorize
```

Same handler, same signed_request shape; we open the same DSR
erasure when a user removes the app.

## D. Business Verification

Required before any of the permissions you need will be granted in
App Review.

You'll be asked for:

- A certificate of incorporation / business registration.
- A tax ID (PIN certificate in Kenya).
- A utility bill or bank statement showing the registered business
  address.
- Optionally a signed verification letter from a director.

Tip: scan everything at 300 DPI. Meta's OCR is picky.

## E. Permissions you need to request

For the social-media side of the platform:

### Messenger

| Permission | Why |
|---|---|
| `pages_messaging` | Receive + reply to Messenger DMs |
| `pages_messaging_subscriptions` | Subscribe to webhook events on the Page |
| `pages_manage_metadata` | Read the Page info needed for routing |
| `pages_read_engagement` | See comments + reactions for ticket creation |

### Instagram

| Permission | Why |
|---|---|
| `instagram_basic` | Connect to the IG Business account |
| `instagram_manage_messages` | Receive + reply to IG DMs |
| `instagram_manage_comments` | Pick up post comments as tickets |

### WhatsApp

| Permission | Why |
|---|---|
| `whatsapp_business_messaging` | Send + receive WhatsApp messages |
| `whatsapp_business_management` | Manage the WABA + templates |

For each permission, Meta wants:

1. **A 1–3 minute screencast** showing the end-to-end use case.
   Easiest: spin up the demo (`make demo`), connect a test FB Page
   per `docs/demo/README.md` §4, send a Messenger DM from another
   account, show it landing in the agent UI inbox and being replied
   to. The recording should match the description you submit.
2. **A written description** of the use case (3–5 sentences).
3. **Step-by-step reviewer instructions** including how to log in
   to the agent UI as a reviewer (use a temporary test account).

## F. Testing

Before submitting, manually verify:

- [ ] `/v1/meta/data-deletion` returns 200 with a forged
      signed_request when `FB_APP_SECRET` matches.
- [ ] `/v1/meta/data-deletion` returns 401 when the HMAC doesn't
      match.
- [ ] `/v1/meta/deletion-status/<code>` renders the HTML page.
- [ ] Privacy Policy URL is reachable without authentication.
- [ ] Data Deletion Instructions URL is reachable without
      authentication.
- [ ] The App Icon renders crisply on a white background.

The handler tests cover the HMAC and parsing edges
(`internal/meta/data_deletion_test.go`). End-to-end the demo runbook
walks through the live flow.

## G. Common review rejections (and how to dodge them)

| Reason | Fix |
|---|---|
| "Use case unclear" | Make the screencast explicit: state the brand name, show the customer side AND the agent side. |
| "Privacy Policy missing required disclosures" | Use the template in this repo — it lists every required section (controller identity, retention, rights, contact). |
| "Data Deletion endpoint not reachable" | The URL must respond from the public internet during review. If you're tunnelling via cloudflared, swap it for a persistent named tunnel BEFORE submission — quick tunnels rotate. |
| "Business Verification documents not matching" | The legal name in your App Settings must EXACTLY match the business registration document. Trailing "Ltd" / "Limited" matters. |
| "Webhook not subscribed" | Re-run the Subscribe call in the Meta dashboard; the verify-token field must equal your `FB_APP_SECRET` (demo) or the dedicated verify token (production). |

## H. After approval

- Switch the app to **Live** in App Settings.
- Update `docs/demo/README.md` §4e — App Roles are no longer the
  gate; anyone whose Page admin connects can use it.
- Rotate `FB_APP_SECRET` to a fresh value via Vault transit (the
  demo's `DemoCrypter` becomes `VaultCrypter` here).
