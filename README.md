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

Native apps and the browser extension are planned; see `DESIGN.md`.

## Try it locally

    go build -o codemail ./cmd/codemail
    ./codemail serve -dev

Open the setup link printed in the log to create the administrator
account, then add a shared domain under Admin. In dev mode the web app is
at http://localhost:8080 and mail is accepted on port 2525, with no
certificates and no sender checks.

## Run it for real

You need a server with a public IP address and ports 25, 80 and 443
reachable. Many home connections block port 25.

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
uses it for both HTTPS and SMTP. To put it behind your own reverse proxy
instead, add `-behind-proxy` and proxy HTTPS to its `-http` port.

Then open `https://mail.example.com`, follow the setup link from the log,
and add `example.com` as a shared domain under Admin.

Codemail only receives mail. Every address on a domain you point at it is
handled by Codemail, so use a domain or subdomain that does not also need
ordinary mailboxes.

## Backup and upgrade

All state is in the data directory. Stop the server and copy it to back
up. To upgrade, replace the binary; the database is migrated on start.

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
