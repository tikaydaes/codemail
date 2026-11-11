# Codemail

Codemail is a self-hosted server that receives email for your domains,
pulls out sign-in codes and confirmation links, and shows them to you the
moment they arrive.

It is one Go binary with an embedded database and web app. It needs no
other services: no database server, no mail server, no push service and
no AI model.

## What it does

- Receives mail over SMTP for any number of domains.
- Finds the code or link in each message with a small trained model that
  ships inside the binary.
- Shows codes live in a web app: copy with one click, see who really sent
  the message, and whether a link goes to the sender's own domain.
- Gives every user several kinds of address: `you@domain`,
  `you+tag@domain`, `anything@you.domain`, names you claim, and
  unguessable addresses generated per service that warn you if they leak.
- Lets users bring their own domains, verified by DNS.
- Signs people in with passkeys only. Registration can be open, by invite
  code, or closed.
- Connects apps and browser extensions with OAuth or a QR code.
- Converts every message to Markdown for display, so mail cannot run
  code, load remote images or track you. The original stays downloadable.
- Deletes mail automatically (7 days by default).

A Flutter app for Android, iOS, macOS, Windows and Linux is in `app/`,
and a browser extension for Chromium browsers and Firefox is in
`extension/`. Neither is published in an app store.

## Try it locally

    go build -o codemail ./cmd/codemail
    ./codemail serve -dev

Open the setup link printed in the log to create the administrator
account, then add a shared domain under Admin. In dev mode the web app is
at http://localhost:8080 and mail is accepted on port 2525, with no
certificates and no sender checks.

## Run it for real

You need a server with a public IP address and ports 25, 80 and 443
reachable from the internet. A small VPS is the dependable choice.

### Hosting at home

Codemail only receives mail, so the usual reasons self-hosted mail fails
at home (blocked outbound port 25, the poor sending reputation of
residential addresses) do not apply. What matters is whether other mail
servers can reach you on port 25, and several things can prevent that:

- **Inbound port 25 is blocked.** Some providers block it on residential
  plans. Some let you turn the block off in your account settings.
- **You share an IP address (CGNAT).** Many mobile, fixed wireless and
  fibre plans put several customers behind one public address, so no
  inbound port works without paying for a public or static IP. Check by
  comparing your router's WAN address with what a "what is my IP" site
  shows: if they differ, you are behind CGNAT.
- **Your address changes.** With a dynamic IP you need dynamic DNS for
  the server's hostname, and mail sent while the address is changing can
  be delayed or bounced.
- **Your plan forbids servers.** Residential terms of service often do,
  even where nothing technically stops you.

Once the server is running, test from outside your network with an online
port checker against port 25 before pointing a domain at it. Provider
policies vary and change, so check your own.

DNS, for a server at `mail.example.com` handling mail for `example.com`:

| Type | Name            | Value               | Why                              |
|------|-----------------|---------------------|----------------------------------|
| A    | mail.example.com | your server's IP   | The web app and the mail server  |
| MX   | example.com     | 10 mail.example.com | Mail for `name@example.com`      |
| MX   | *.example.com   | 10 mail.example.com | Mail for `anything@you.example.com` |

The web app and the mail domain can be the same name (for example both
`example.com`); then the A record goes on `example.com` and the MX
records point at it.

Start it:

    ./codemail serve -hostname mail.example.com -data /var/lib/codemail

Codemail obtains and renews its own certificate from Let's Encrypt and
uses it for both HTTPS and SMTP.

### Behind a reverse proxy, with Docker

If a reverse proxy already serves HTTPS on the machine, run Codemail with
`-behind-proxy`. It then serves plain HTTP on its `-http` address for the
proxy, trusts the proxy's `X-Real-IP` header, and encrypts SMTP with a
self-signed certificate it creates itself (or one you pass with
`-smtp-cert` and `-smtp-key`). Mail servers encrypt opportunistically
and do not check the certificate.

If mail is delivered to a different name from the web address (for
example the web address goes through a CDN), give that name with
`-mail-host`; it is what MX records must point at.

`deploy/docker-compose.yml` is a ready-made setup for this. Images are
published to `ghcr.io/tikaydaes/codemail`.

Then open `https://mail.example.com`, follow the setup link from the log,
and add `example.com` as a shared domain under Admin.

Codemail only receives mail. Every address on a domain you point at it is
handled by Codemail, so use a domain or subdomain that does not also need
ordinary mailboxes.

## Backup and upgrade

All state is in the data directory. To back up while the server is
running:

    ./codemail backup -data /var/lib/codemail /backups/codemail-$(date +%F).db

writes a consistent copy of the database. To upgrade, replace the binary;
the database is migrated on start.

## PostgreSQL (optional)

Codemail uses an embedded SQLite database by default and needs nothing
else. If you would rather use PostgreSQL, give its URL with `-db` or the
`CODEMAIL_DB` environment variable:

    ./codemail serve -hostname mail.example.com -db postgres://codemail:secret@localhost/codemail

To move an existing installation, stop the server and copy the data into
an empty PostgreSQL database (or back again):

    ./codemail migrate-db -from /var/lib/codemail/codemail.db -to postgres://codemail:secret@localhost/codemail

Back PostgreSQL up with `pg_dump`. The data directory is still used for
certificates.

## Improving extraction from corrections

When a user clicks "Wrong code?" and corrects a message, the correction
is stored. An admin can download them from the Admin page and train on
them alongside labelled mail:

    ./codemail train -corrections corrections.jsonl <mail directory>

This writes new weights into the source tree; rebuild to use them.

If you are locked out with shell access to the server:

    ./codemail recovery -data /var/lib/codemail <username>

prints a one-time recovery code.

## Development

    go test ./...        # API and conversion tests
    e2e/run.sh           # drives the web app in a headless browser

The web app in `internal/web/static` is plain HTML, CSS and JavaScript
modules with no build step. `DESIGN.md` describes the architecture.

## Licence

Codemail is free software, released under the GNU Affero General Public
License, version 3. See `LICENSE`. If you run a modified version as a
service, you must offer its source to the people who use it.
