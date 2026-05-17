# Privacy Policy — Contact Centre Platform

_Last updated: {{LAST_UPDATED}}_

This is a fill-in-the-blanks template. Replace every `{{PLACEHOLDER}}` with
your organisation's actual values, host the resulting page over HTTPS at a
stable URL, and submit that URL when Meta asks for your **Privacy Policy
URL** during App Review.

The template is opinionated towards compliance with the **Kenya Data
Protection Act 2019 (DPA)**. If you serve users outside Kenya the
template still works as a baseline; check local rules (GDPR, POPIA,
etc.) before publishing.

---

## 1. Who we are

**{{COMPANY_LEGAL_NAME}}** ("we", "us", "the Operator") is registered
in Kenya under company number {{COMPANY_REG_NO}}. Our registered
office is {{COMPANY_ADDRESS}}.

We are the **Data Controller** for the personal data described in this
policy. Our **Data Protection Officer** is {{DPO_NAME}}, contactable at
{{DPO_EMAIL}}.

We are registered with the **Office of the Data Protection
Commissioner (ODPC)** under registration number {{ODPC_REG_NO}}.

## 2. What this policy covers

This policy describes how we handle personal data when you:

- Send us a message via Facebook Messenger, Instagram Direct,
  WhatsApp, X (Twitter), or our website chat widget.
- Telephone us, where the call is handled through this platform.
- Visit a page where our chat widget is embedded (we set a first-party
  cookie to remember your conversation).

## 3. What we collect

| Source | What we collect |
|---|---|
| Facebook / Instagram / WhatsApp DMs | Your platform-scoped user id (PSID / IGSID / WAID), your display name as shown to us by the platform, the content of your messages, timestamps |
| Website chat widget | A visitor UUID (in a first-party cookie, 12-month TTL), your message content, the page URL where the chat was opened |
| Voice (Africa's Talking) | Your phone number, call duration, an optional voicemail recording |
| All channels | Any attachments you choose to send (images, PDFs, etc.), our agents' notes about your conversation, the outcome (ticket state) |

We **do not** collect: your password, your banking details, your
government ID, your location beyond what's in your message body, or
data from any social platform other than the channels listed above.

## 4. Why we collect it (lawful basis under DPA s.30)

- **Performance of a service you requested**: when you message us, we
  process the message to respond to you. Without this we cannot reply.
- **Legitimate interest**: keeping a brief audit trail so our staff
  can pick up where a colleague left off, and so we can investigate
  complaints about our own service.
- **Legal obligation**: where we must retain records under tax,
  consumer-protection, or sector-specific rules.

## 5. Who sees it

- Our agents handling your conversation (named individuals with
  per-tenant access, RBAC-enforced).
- Our supervisors when reviewing service quality.
- Our Data Protection Officer when responding to a subject access or
  erasure request.
- **No third parties for marketing purposes.** We do not sell or
  share personal data for advertising.

The platform is operated on infrastructure within
{{HOSTING_JURISDICTION}}. Where data leaves Kenya, transfers are
covered by {{TRANSFER_MECHANISM}}.

## 6. How long we keep it

| Data | Retention |
|---|---|
| Message bodies, attachments, ticket history | {{MESSAGES_RETENTION_DAYS}} days from the last activity, then auto-deleted |
| Audit log (who-did-what) | {{AUDIT_RETENTION_DAYS}} days; required for compliance |
| Subject Access / Erasure request records | 7 years (DPA s.43 record-keeping) |

You can ask us to delete your data sooner — see section 8.

## 7. Cookies

Our website chat widget sets one first-party cookie, `cc_visitor`,
holding a random UUID with no other identifying information. It lasts
12 months. You can clear it at any time from your browser settings;
clearing it ends your current widget session without affecting any
DMs you've sent us on social platforms.

We do **not** set any tracking, analytics, or advertising cookies via
the chat widget.

## 8. Your rights (DPA Part V)

You have the right to:

- **Access** the personal data we hold about you (s.26).
- **Correct** inaccurate data (s.27).
- **Delete** your data, subject to legal retention rules (s.28).
- **Restrict** processing while a dispute is being investigated (s.29).
- **Object** to processing on legitimate-interest grounds (s.30).
- **Receive a portable copy** of data you provided (s.34).
- **Lodge a complaint** with the ODPC at <https://www.odpc.go.ke>.

To exercise any of these rights, email {{DPO_EMAIL}} with proof of
identity. We respond within **30 days** (s.32). If your request was
delivered via the social platforms (e.g. Meta forwarded your data
deletion request to us), our system opens a request automatically —
see section 9.

## 9. Meta integration & data deletion callback

We use Meta's official APIs (Messenger, Instagram Direct, WhatsApp
Cloud) to receive your messages. Meta's policies require us to
implement a **data deletion callback**: when you remove our app from
your Facebook or Instagram account, Meta sends us your platform user
id and we delete the conversation history attached to that id within
30 days.

If you would prefer to request deletion directly without going through
Meta, email {{DPO_EMAIL}} with your platform handle (e.g. your
Facebook profile URL) and we'll process it as a Subject Erasure
Request under s.28.

## 10. Security

We protect your data with:

- TLS in transit, AES-256-GCM at rest for credentials.
- Per-tenant data isolation (your messages are never visible to
  another organisation using this platform).
- Append-only audit logging — every agent action against your data
  is recorded.
- Annual penetration testing and a published vulnerability disclosure
  programme at {{VDP_URL}}.

## 11. Children

This platform is not intended for users under 13. If we become aware
that we hold data on a child under 13 without verifiable parental
consent we delete it immediately.

## 12. Changes to this policy

We review this policy at least annually. Material changes are
announced via {{CHANGE_NOTIFICATION_CHANNEL}}. The "Last updated"
date at the top reflects the latest revision.

## 13. Contact

| Reason | Email |
|---|---|
| Subject access / erasure / portability | {{DPO_EMAIL}} |
| Security report | {{SECURITY_EMAIL}} |
| Anything else | {{GENERAL_EMAIL}} |

You can also write to us at the registered office in section 1.
