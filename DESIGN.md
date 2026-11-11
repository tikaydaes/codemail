# Codemail design

A self-hosted service that receives email for your domains, extracts
sign-in codes and confirmation links, and delivers them to your devices.

## 1. Constraints

- **No external services at runtime.** No database server, mail server,
  push service (APNs, FCM), or model API. Go libraries are fine.
- **One static Go binary**, no cgo, cross-compiled. State is one data
  directory.
- **Receive only.** The server never sends email.
- **API first.** Every client (web, desktop, mobile, extension) uses the
  same HTTP API.
- **Single node by default.** Embedded SQLite needs nothing else.
  PostgreSQL is an optional backend for people who want it; it is the one
  permitted external service and is never required.

## 2. System overview

    Internet MTAs ──SMTP :25──▶ ┌──────────────── codemail ────────────────┐
                                │ smtpd → auth check → resolve → parse     │
                                │       → extract → store → hub            │
    Browsers / apps ─HTTPS :443▶│ api (REST + SSE) · web (embedded SPA)    │
                                │ webauthn · acme · dnscheck · janitor     │
                                │ SQLite (data/codemail.db)                │
                                └──────────────────────────────────────────┘

| Package    | Responsibility                                               |
|------------|--------------------------------------------------------------|
| `smtpd`    | Inbound SMTP, STARTTLS, limits, rejects unknown recipients   |
| `mailauth` | SPF, DKIM, DMARC evaluation of each message                  |
| `mailmsg`  | MIME parsing into extractor input                            |
| `extract`  | Candidate finder and trained scorer                          |
| `store`    | Storage interface, with SQLite and PostgreSQL backends       |
| `hub`      | In-process fan-out of new-message events                     |
| `api`      | REST endpoints, sessions, device tokens, SSE stream          |
| `passkey`  | WebAuthn registration and sign-in, recovery codes            |
| `web`      | The built SPA, embedded with `go:embed`                      |
| `acme`     | Certificates for HTTPS and STARTTLS                          |
| `dnscheck` | MX and TXT lookups for domain verification                   |
| `janitor`  | Expiry of messages, sessions, invites and pairing codes      |

## 3. Mail pipeline

1. **Connect.** Per-IP connection and rate limits. STARTTLS offered.
2. **RCPT TO.** The address is resolved to a user (section 4). Unknown
   recipients get `550`, so nothing is stored for strangers.
3. **DATA.** Size cap (default 10 MB) and a per-user daily message cap.
4. **Sender check.** SPF, DKIM and DMARC are evaluated and the result is
   stored. Failing mail is still accepted by default (an admin setting can
   reject DMARC failures); the result drives the "sender verified" badge
   and what the extension will auto-offer.
5. **Extract.** Code, link, service name and confidence (section 5).
6. **Store** with an expiry time, then **publish** to the hub.

Mail with no code or link is kept in "Other mail" with the same expiry and
never triggers a notification.

## 4. Addressing and domains

| Address                    | Goes to                   | Requires                          |
|----------------------------|---------------------------|-----------------------------------|
| `anything@owned.example`   | the domain's owner        | user-owned, verified domain       |
| `alias(+tag)@shared`       | whoever claimed the alias | shared domain, `allow_aliases`    |
| `username(+tag)@shared`    | that user                 | shared domain, `allow_mailbox`    |
| `anything@username.shared` | that user                 | shared domain, `allow_subdomains` |

- **Shared domains** are added by an admin, who sets the three flags and a
  per-user limit on claimed aliases. Subdomain addressing needs a wildcard
  MX record.
- **User-owned domains.** A user adds a domain and is shown two records:
  an MX pointing at the server and a TXT ownership token at
  `_codemail.<domain>`. Mail is accepted once both are found. Both are
  re-checked daily; a domain that fails for 7 days stops receiving. The
  TXT token stops a second user claiming a domain whose MX already points
  here.
- **Generated aliases.** A user names a service ("Northwind Bank") and
  the server generates an unguessable address for it, such as
  `quiet-fern-4821@shared`. The name is stored as the alias's label and
  shown on every code that arrives there. Mail to that alias from an
  unrelated sender is flagged, since it means the address leaked. The
  extension's "new address for this site" uses this. Generated aliases
  count towards the per-user alias limit; an admin can set a separate,
  higher limit for them.
- **Reserved names.** `postmaster`, `abuse`, `admin` and similar cannot be
  usernames or aliases; `postmaster@` on shared domains goes to admins.
- Usernames are DNS-label safe so they work as mailbox names and
  subdomains.

## 5. Extraction

No LLM. Regular expressions list candidates from both the plain-text and
HTML renderings; a logistic model scores each one from candidate and
message features. Weights are embedded (`internal/extract/weights.json`)
and fitted by `codemail train` on labelled mail. Current accuracy on
unseen senders: codes 91% precision and 97% recall, links 92% and 78%.

**Corrections.** "Wrong code?" on any message lets the user pick the
right value, or say there is none. The correction fixes that message at
once and is stored as a label. Admins export corrections (with the
messages, kept past normal expiry only if the user agreed) to retrain.
Nothing leaves the server.

## 6. Storage

`store.Store` is a Go interface (users, passkeys, domains, aliases,
messages, and so on); the rest of the server depends only on it. One
shared test suite runs against every backend.

| Backend    | When                         | Notes                              |
|------------|------------------------------|------------------------------------|
| SQLite     | Default                      | Pure Go driver, one file, no setup |
| PostgreSQL | Optional, `-db postgres://…` | For larger or existing deployments |

Both are SQL, so they share one schema and most queries, with a thin
dialect layer and per-backend migrations. A key-value engine (RocksDB, or
Pebble, its pure-Go equivalent) was considered and not chosen: RocksDB
needs cgo, which breaks the static binary, and either would mean
hand-building the indexes and queries that SQL provides and that
PostgreSQL support needs anyway. `codemail migrate-db` copies a database
from one backend to the other.

### Tables

| Table           | Key columns                                                    |
|-----------------|----------------------------------------------------------------|
| `users`         | username, is_admin, retention, created_at                      |
| `passkeys`      | user, credential id, public key, sign count, name, last used   |
| `recovery_codes`| user, code hash, used_at                                       |
| `sessions`      | user, token hash, expires_at (web cookies)                     |
| `devices`       | user, name, platform, token hash, last_seen, revoked_at        |
| `oauth_codes`   | one-time code hash, user, client, kind (authorization or QR pairing), PKCE challenge, expires_at |
| `invites`       | code, max_uses, uses, expires_at, created_by                   |
| `domains`       | name, owner (null = shared), verified, verify_token, flags     |
| `aliases`       | domain, local part, user, label, generated, expected sender    |
| `messages`      | user, rcpt, from, subject, service, code, link, confidence,    |
|                 | auth result, raw, received_at, expires_at, used_at             |
| `corrections`   | message, kind, correct value, created_at                       |
| `site_links`    | user, sender domain, site domain (learned by the extension)    |
| `settings`      | registration mode, limits, retention cap                       |
| `audit_log`     | actor, action, target, at                                      |

Tokens are 256-bit random values stored only as SHA-256 hashes.

## 7. Authentication

- **Passkeys only** (WebAuthn, discoverable credentials, user
  verification required). Sign-in needs no username. The relying-party ID
  is the server's HTTPS hostname; mail domains are unrelated to it.
- **Registration** is `open`, `invite` or `closed`, set by an admin. The
  first account is created through a one-time setup link printed when the
  server first starts, and becomes the admin.
- **Recovery.** Eight single-use recovery codes are shown once at sign-up.
  Users are prompted to add a second passkey. As a last resort an admin
  issues a one-time recovery link.
- **Web** uses an HttpOnly, SameSite=Strict session cookie.
- **Native apps and the extension** never handle passkeys, because
  platforms tie passkeys to domains compiled into an app. They use
  **OAuth 2.0 authorization code with PKCE** against the server's own URL:
  1. The user enters the server address.
  2. The app opens `https://<server>/oauth/authorize` in the system
     browser. The user signs in with a passkey and approves the named
     device.
  3. The browser redirects back to the app (loopback port on desktop, app
     link on mobile, the extension's redirect URL) with a one-time code.
  4. The app exchanges the code and its PKCE verifier at `/oauth/token`
     for an access token and a refresh token.
  Clients are public and need no registration: the redirect must be a
  loopback address, a known extension origin or the app's registered
  scheme. The server publishes `/.well-known/oauth-authorization-server`
  so clients discover the endpoints from the address alone.
- **QR pairing** is the second way in, for a device where typing an
  address and signing in again is awkward (mainly phones). Devices → Add
  device in the web app shows a QR code holding the server address and a
  one-time code valid for 2 minutes. The app scans it and exchanges the
  code at `/oauth/token` (a pairing-code grant), and the web page asks the
  already signed-in user to confirm the device's name before tokens are
  issued. Both routes end with the same tokens and the same entry in the
  device list.
- Devices are listed with last-seen time and can be revoked individually.

## 8. API

All under `/api/v1`, JSON, authenticated by session cookie or
`Authorization: Bearer <access token>`.

| Area      | Endpoints                                                        |
|-----------|------------------------------------------------------------------|
| Auth      | `POST passkey/register/{begin,finish}`, `passkey/login/{begin,finish}`, `recovery`, `logout`; `GET me` |
| Passkeys  | `GET/POST/DELETE passkeys`                                       |
| OAuth     | `GET /oauth/authorize`, `POST /oauth/token`, `POST /oauth/revoke` (outside `/api/v1`) |
| Devices   | `GET devices`, `DELETE devices/{id}`, `POST devices/pairing-code`, `POST devices/pairing-code/{id}/confirm` |
| Messages  | `GET messages?kind=code|other&since=&q=`, `GET messages/{id}` (body as Markdown), `GET messages/{id}/raw`, `PATCH messages/{id}` (used), `DELETE messages/{id}`, `POST messages/{id}/correction` |
| Events    | `GET events` (SSE)                                               |
| Addresses | `GET/POST/DELETE aliases`, `POST aliases/generate`, `GET address-options` |
| Domains   | `GET/POST/DELETE domains`, `POST domains/{id}/check`             |
| Extension | `GET codes/for-site?domain=`, `POST site-links`                  |
| Admin     | `settings`, `invites`, `users`, `shared-domains`, `status`, `corrections/export` |

## 9. Live delivery

- `GET /api/v1/events` is a Server-Sent Events stream. Event IDs are
  message IDs, so a client reconnecting with `Last-Event-ID` receives
  anything it missed. A heartbeat every 25 seconds keeps connections open.
- Marking a code used on one device greys it out on the others.
- Because no push service is used:

| Client        | When closed                                                  |
|---------------|--------------------------------------------------------------|
| Web           | Nothing; live while the tab is open                          |
| Desktop apps  | Run in the menu bar or tray and stay connected               |
| Extension     | Background worker reconnects when the browser is running     |
| Android       | Foreground service keeps the stream open, with a permanent notification |
| iPhone        | Nothing; the app catches up the moment it is opened          |

## 10. Clients

| Client    | Stack                               | Role                          |
|-----------|-------------------------------------|-------------------------------|
| Web       | Plain HTML, CSS and JavaScript modules, embedded in the binary | Everything, including admin and account management |
| Apps      | Flutter, one codebase in `app/` for Android, iOS, macOS, Windows and Linux | Codes, messages, addresses, notifications; tray and auto-copy on desktop; background service on Android |
| Extension | WebExtension in `extension/` (Chromium browsers, Firefox) | Offer codes in sign-in fields |

**Distribution.** No main app stores. Apps are downloads attached to
GitHub releases; the Android app carries no Google libraries so it can go
on F-Droid; the iOS app is an unsigned package for sideloading or building
yourself. The browser extension is likewise distributed directly.

Account management (passkeys, domains, admin) lives only in the web app;
native apps link to it.

**Desktop behaviour.** A new code raises a system notification and, if
enabled, is copied to the clipboard. The clipboard is cleared after a
chosen time (default 60 seconds), but only if it still holds the code.
Confirmation links are never opened automatically.

**Extension behaviour.**
- It connects straight to the server with its own OAuth token; no desktop
  app is needed.
- On a field marked `autocomplete="one-time-code"` (or one that looks like
  a code field) it offers the newest code inline, and waits visibly if the
  email has not arrived.
- A code is offered inline only if the sender is verified and the
  sender's domain matches the site's registrable domain, or the user has
  previously used that sender's codes on this site (`site_links`). Other
  recent codes are available from the toolbar popup only. This is the
  defence against a phishing page harvesting a code.
- On sign-up forms it can fill a new address for the site
  (`sitename@username.shared` or a claimed alias).

## 11. UI

| Screen                      | Function                                          |
|-----------------------------|---------------------------------------------------|
| Web: Codes                  | Newest code large with Copy; earlier codes and links; selected message with sender check, confidence, original, Wrong code?, Delete |
| Web: Other mail             | Same list and detail for mail without a code      |
| Web: Addresses and domains  | Built-in address forms, claimed aliases, own domains with DNS records and live status |
| Web: Devices                | Paired devices, last seen, revoke; Add device (QR) |
| Web: Settings               | Passkeys, recovery codes, retention, delete account |
| Web: Admin                  | Server status, registration mode, invites, shared domains, users, corrections export |
| Web: Sign in and register   | Passkey sign-in; username + invite code; recovery codes |
| Phone: Codes / Message / Pair | Feed, message with link safety summary, first-run pairing |
| Desktop: menu bar app       | Latest code, recent list, auto-copy, preferences  |
| Browser extension           | Inline suggestion, toolbar popup, new address     |

Display rules: codes are grouped for reading (`834 201`) and copied
without spaces. Codes older than 10 minutes are greyed out. Every link
shows the domain it opens and whether that matches the sender.

## 12. Security and privacy

- **Message bodies are Markdown.** The server converts every message
  to Markdown (headings, paragraphs, emphasis, lists, links) and that is
  the only form clients display, so a message looks the same everywhere.
  Scripts, styles, images, hidden preview text and layout tables are
  dropped in the conversion, so a displayed message cannot run code, load
  remote content or track the reader. Clients render Markdown to native
  text or DOM nodes and never parse HTML. The original message is stored
  until it expires and can be downloaded.
- Admins manage accounts but have no API to read other users' mail. They
  do control the server and its database, so this is policy, not
  cryptography.
- Mail is not encrypted at rest in the first version; the server must read
  it to extract codes. Use disk encryption. Short retention (default 7
  days, user-adjustable up to an admin cap) limits exposure.
- Rate limits on SMTP, sign-in, OAuth and invite attempts. All
  administrative actions go to the audit log.
- Strict Content-Security-Policy; no third-party scripts, fonts or
  analytics in the shipped web app.

## 13. Deployment

- **DNS:** an A/AAAA record for the server hostname; an MX record on each
  mail domain pointing at it; a wildcard MX for subdomain addressing.
- **Ports:** 25 (SMTP), 443 (HTTPS), 80 (ACME and redirect). Many home
  connections block port 25, so a small VPS is the expected host.
- **TLS:** built-in ACME obtains and renews one certificate for the server
  hostname, used for HTTPS and STARTTLS. `-behind-proxy` turns this off
  for setups with their own TLS termination.
- **Configuration:** flags or environment for listeners, hostname and data
  directory; everything else is set in the admin UI and stored in the
  database.
- **Backup:** copy the data directory (or `codemail backup` for a
  consistent snapshot). **Upgrade:** replace the binary; migrations run at
  start.

## 14. Status and build order

Built and tested:

- Storage interface with SQLite and PostgreSQL backends, `migrate-db` to
  copy between them and `backup` for SQLite.
- SMTP receiver: address resolution, per-address and per-user limits,
  generated-alias leak detection, extraction, expiry.
- HTTP API (everything in section 8), passkey registration and sign-in,
  recovery codes, sessions (listed and revocable per browser), the live
  event stream.
- OAuth with PKCE, QR pairing, device list and revocation.
- Domain claims with DNS verification and daily re-checks.
- Admin: registration modes, invites, shared domains, users, policy,
  corrections export, and `codemail train -corrections` to learn from it.
- Web app: all screens in section 11.

Tests: `go test ./...` covers the API end to end with a virtual passkey
authenticator. `e2e/run.sh` drives the web app in a headless browser.

Written but not yet exercised against the real internet: ACME
certificates, STARTTLS, and SPF/DKIM/DMARC lookups (tests use a stub).

Differences from the plan above:

- The web app is plain JavaScript modules with no build step, not Svelte,
  so `go build` alone produces the whole server.
- Message bodies are converted to Markdown rather than shown as sanitised
  HTML (see section 12).

Still to build, in order:

1. Browser extension: built for Chromium browsers and Firefox in
   `extension/` (see its README). Safari is not supported yet.
2. First real deployment; verify certificates and sender checks.
3. Apps: a first Flutter version exists in `app/` (see its README). Still
   to do: real-device testing, release signing, app icons, running the
   Android connection in the service itself so it survives the app being
   swiped away.
4. Ongoing: link recall, promo codes.

## 15. Risks

- **iPhone delivery** is foreground-only under the no-push rule. This is
  the weakest part of the product and cannot be engineered around.
- **Port 25 reputation and blocking** affect whether senders can reach a
  self-hosted server at all.
- **The apps are one Flutter codebase**, chosen over five native ones to
  keep maintenance manageable. The cost is a less native desktop feel.
- **Extraction misses** are mitigated by keeping every message viewable
  and by the corrections loop.
