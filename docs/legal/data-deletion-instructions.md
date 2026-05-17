# Data deletion — user instructions

_Last updated: {{LAST_UPDATED}}_

This document goes at the **Data Deletion Instructions URL** field in
your Meta App Review submission. It explains how an end user can have
their data removed from your platform.

You don't need both this page **and** the callback endpoint — Meta
accepts either. We recommend submitting both: the callback URL handles
the automated flow when users remove your app from Facebook /
Instagram; this page handles users who never installed your app (e.g.
visitors who only DMed your Page).

---

## How to request deletion

You have three options, and any one of them works.

### Option 1 — Remove the app from your Facebook account

1. Open Facebook → **Settings & Privacy → Settings**.
2. **Apps and Websites** → find {{APP_DISPLAY_NAME}} → **Remove**.
3. Facebook automatically notifies us. We delete the conversation
   history tied to your Facebook user id within 30 days.

The same flow exists on Instagram: **Settings → Apps and Websites →
Active**.

### Option 2 — Email us

Email {{DPO_EMAIL}} from the address tied to your Facebook account,
or include a screenshot showing the Facebook profile URL you used to
contact us. We treat this as a Subject Erasure Request under Kenya
Data Protection Act 2019 s.28 and respond within 30 days.

### Option 3 — Visit the status page

When Meta forwards a deletion request to us we issue a **confirmation
code**. The URL Meta shows you after your removal action looks like:

```
{{PUBLIC_BASE_URL}}/v1/meta/deletion-status/<code>
```

Bookmark that URL — it shows the live status of your request without
needing an account.

## What we delete

- Every message you sent us on any of our connected channels.
- Every reply our agents sent to you.
- Attachments you uploaded (images, PDFs).
- The internal ticket(s) created from your conversation.
- The customer record keyed on your platform user id.

## What we keep (anonymised)

The Kenya DPA s.43 requires us to retain a small audit footprint:

- The date your request was opened and resolved.
- The fact that "a Subject Erasure Request was fulfilled" (no name,
  no message content).
- Aggregate metrics (counts, no identifiers) for service quality
  reporting.

This residue cannot be linked back to you.

## How long it takes

| Day | What happens |
|---|---|
| 0 | We receive your request (via Meta callback, email, or both). |
| 0–1 | Our Data Protection Officer confirms identity and queues the deletion. |
| 1–7 | Conversation history + attachments hard-deleted from primary storage. |
| 7–30 | Encrypted backups containing your data expire under their normal rotation. |
| 30 | All deletion complete. Status page shows **fulfilled**. |

## Lodging a complaint

If you're unhappy with how we handled your request, you can lodge a
complaint with the **Office of the Data Protection Commissioner**:

- Web: <https://www.odpc.go.ke>
- Email: info@odpc.go.ke
- Phone: +254 20 4900800

## Contact

Data Protection Officer: {{DPO_EMAIL}}
