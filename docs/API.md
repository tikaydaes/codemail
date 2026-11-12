# Codemail API

Everything the web app, the apps and the browser extension use. All paths
are under `/api/v1` unless they start with `/oauth` or `/.well-known`.
Requests and replies are JSON. Errors are `{"error": "message"}` with a
4xx or 5xx status. Times are Unix seconds; `0` means "not set".

## Authentication

A request is authenticated in one of two ways:

- **Browser session**: the `cm_session` cookie set by signing in with a
  passkey or recovery code. State-changing requests must come from the
  server's own origin.
- **Device token**: `Authorization: Bearer <access token>`, obtained with
  OAuth or QR pairing (below).

Endpoints marked *browser* need a browser session and refuse device
tokens. Endpoints marked *admin* also need the user to be an admin.

## Signing in (browser)

| Method and path | Purpose |
|---|---|
| `GET /server` | Public. `registration` (`open`, `invite`, `closed`), `needs_setup`, `mail_host`. |
| `POST /passkey/register/begin` | Body `username`, and `invite` or `setup` as required. With a session, adds a passkey to the signed-in account instead. Returns `session` and WebAuthn `publicKey` options. |
| `POST /passkey/register/finish` | Body `session`, `credential`, optional `name`. For a new account returns `user` and one-time `recovery_codes`, and sets the cookie. |
| `POST /passkey/login/begin` | Returns `session` and `publicKey` options for a discoverable credential. |
| `POST /passkey/login/finish` | Body `session`, `credential`. Sets the cookie; returns `user`. |
| `POST /recovery` | Body `username`, `code`. Uses up one recovery code and sets the cookie. |
| `POST /logout` | Ends the current browser session. |

## Account

| Method and path | Purpose |
|---|---|
| `GET /me` | `user` (`id`, `username`, `is_admin`, `retention_hours`) and `server` limits. |
| `PATCH /me` *browser* | Body `retention_hours` (0 = server default). |
| `DELETE /me` *browser* | Deletes the account and everything it owns. |
| `GET /passkeys` *browser* | The account's passkeys. |
| `DELETE /passkeys/{id}` *browser* | Removes one; refused for the last passkey. |
| `POST /recovery-codes` *browser* | Replaces the recovery codes and returns the new ones. |
| `GET /sessions` *browser* | Signed-in browsers: `id`, `label`, `created_at`, `last_seen`, `current`. |
| `DELETE /sessions/{id}` *browser* | Signs that browser out. |

## Messages

A message has `id`, `kind` (`code` if it has a code or link, else
`other`), `rcpt`, `from`, `subject`, `service`, `code`, `link`,
`link_host`, `link_matches_sender`, `confidence`, `auth` (`pass`, `fail`,
`none`), `sender_domain`, `flags` (may contain `unexpected_sender`),
`received_at`, `expires_at`, `used_at`.

| Method and path | Purpose |
|---|---|
| `GET /messages?kind=&q=&limit=&before=&after=` | Newest first; `before` pages backwards, `after` returns newer ones oldest first. `limit` up to 200. |
| `GET /messages/{id}` | The message plus `mail_from`, `auth_detail` and `markdown` (its body as Markdown). |
| `GET /messages/{id}/raw` | The original email. |
| `GET /messages/{id}/candidates` | Everything in the message that could be its code or link: `kind`, `value`, `score`, `context`. |
| `POST /messages/{id}/correction` | Body `kind` (`code` or `link`) and `value` (empty means there is none). Updates the message and records the correction. |
| `PATCH /messages/{id}` | Body `used` (true or false). |
| `DELETE /messages/{id}` | Deletes it. |
| `GET /events` | Server-Sent Events: `message`, `update` (data is a message) and `delete` (data is `{"id"}`). Event IDs on `message` are message IDs; send `Last-Event-ID` to receive what was missed. A comment line is sent every 25 seconds. |

## Addresses and domains

| Method and path | Purpose |
|---|---|
| `GET /addresses` | `username`, `shared_domains` and their rules, the user's own `domains`, and `aliases`. |
| `POST /aliases` | Body `local`, `domain_id`, optional `label`. Claims an address. |
| `POST /aliases/generate` | Body `label`, optional `domain_id`. Creates an unguessable address for one service. |
| `PATCH /aliases/{id}` | Body `label`. |
| `DELETE /aliases/{id}` | Releases the address. |
| `POST /domains` *browser* | Body `name`. Adds the user's own domain, unverified, and returns the DNS `records` to publish. |
| `POST /domains/{id}/check` *browser* | Looks the records up now; each record reports `found`. |
| `DELETE /domains/{id}` *browser* | Removes the domain. |

## Devices, OAuth and pairing

Apps and extensions are public OAuth clients using the authorization code
flow with PKCE (S256). The server publishes its endpoints at
`/.well-known/oauth-authorization-server`.

1. Open `/oauth/authorize?response_type=code&client_id=…&redirect_uri=…&code_challenge=…&code_challenge_method=S256&state=…&device_name=…&platform=…`
   in a browser. Allowed redirects: `http://127.0.0.1` or `localhost` on
   any port, `codemail://…`, and the browsers' extension redirect hosts.
2. The user signs in and approves; the browser returns to the redirect
   with `code` and `state` (or `error=access_denied`).
3. `POST /oauth/token` (form-encoded) with
   `grant_type=authorization_code`, `code`, `code_verifier`,
   `redirect_uri`. The reply has `access_token` (valid one hour),
   `refresh_token`, `token_type` and `expires_in`.
4. Renew with `grant_type=refresh_token` and `refresh_token`. Refresh
   tokens rotate on every use; the one just replaced keeps working for a
   minute.
5. `POST /oauth/revoke` with `token` (either token) disconnects the device.

QR pairing, for phones:

| Method and path | Purpose |
|---|---|
| `POST /devices/pairing-code` *browser* | Returns `id`, `code`, `uri` (`codemail://pair?server=…&code=…`) and `expires_at` (two minutes). |
| `GET /devices/pairing-code/{id}/qr.png?code=` *browser* | The `uri` as a QR code. |
| `GET /devices/pairing-code/{id}` *browser* | `claimed`, `device_name`, `platform`, `confirmed`. |
| `POST /devices/pairing-code/{id}/confirm` *browser* | Approves the device that claimed the code. |
| `POST /oauth/token` | `grant_type=urn:codemail:params:oauth:grant-type:pairing-code`, `code`, `device_name`, `platform`. Returns `authorization_pending` until confirmed, then tokens. |

| Method and path | Purpose |
|---|---|
| `GET /devices` | Connected apps and extensions. |
| `DELETE /devices/{id}` *browser* | Disconnects one. |
| `POST /oauth/approve` *browser* | Used by the `/oauth/authorize` page; returns the `redirect` to send the browser to. |

## Browser extension

| Method and path | Purpose |
|---|---|
| `GET /codes/for-site?domain=` | Unused code and link messages from the last 15 minutes. Each has `match`: `sender` if the verified sender's domain is the site's, `learned` if the user linked that sender to the site, otherwise empty. Only matched messages should be offered in a page. |
| `POST /site-links` | Body `message_id`, `domain`. Remembers that this verified sender's codes are used on this site. |

## Admin

All *admin*.

| Method and path | Purpose |
|---|---|
| `GET /admin/status` | Counts, storage size, time of the last message. |
| `GET /admin/settings`, `PUT /admin/settings` | `registration`, `max_retention_hours`, `reject_auth_fail`. |
| `GET /admin/invites`, `POST /admin/invites`, `DELETE /admin/invites/{code}` | Invite codes; create with `max_uses` and `expires_days` (0 = unlimited or never). |
| `GET /admin/users`, `PATCH /admin/users/{id}`, `DELETE /admin/users/{id}` | List, set `is_admin`, delete. |
| `POST /admin/users/{id}/recovery-code` | Issues one extra recovery code for that user. |
| `GET /admin/domains`, `POST /admin/domains`, `PATCH /admin/domains/{id}`, `DELETE /admin/domains/{id}` | Shared domains and their rules: `allow_mailbox`, `allow_aliases`, `allow_subdomains`, `max_aliases`, `max_generated`. |
| `GET /admin/corrections` | Users' corrections with their messages, one JSON object per line, for `codemail train -corrections`. |
| `GET /admin/audit?limit=&before=` | Recorded actions, newest first: `id`, `actor`, `action`, `target`, `at`. |
