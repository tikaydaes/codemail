# Codemail

Codemail is a self-hosted server that receives email for your domains,
pulls out sign-in codes and confirmation links, and shows them to you the
moment they arrive.

You can run it yourself, or use the public server at
[codegrab.me](https://codegrab.me). Read
[Whose server?](#whose-server) before you choose.

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
- Takes codes from a mailbox you already have: forward them from it, or
  let the server look in it over IMAP or JMAP. See
  [Codes from a mailbox you already have](#codes-from-a-mailbox-you-already-have).
- Deletes mail automatically (7 days by default).

A Flutter app for Android, iOS, macOS, Windows and Linux is in `app/`,
and a browser extension for Chromium browsers and Firefox is in
`extension/`. The apps are not published in an app store; the Firefox
version of the extension is listed on addons.mozilla.org.

## Whose server?

Whoever runs a Codemail server can read every message it receives. For
this kind of mail that means more than it sounds: a sign-in code or a
password-reset link is often all it takes to get into an account. The
operator of your server, and anyone who breaks into it, can take over any
account that sends its codes there. Nothing in Codemail can prevent that.
The mail arrives unencrypted, and the server has to read it to find the
code.

So the safe way to use Codemail is to run it yourself, on a machine you
control. That is what it is built for.

**codegrab.me** is a public server run by the Codemail developers, for
people who cannot or do not want to host. The apps and the extension
offer it as the default server, and you are welcome to use it. Do so
knowing what you are trusting:

- The people who run it are able to read your mail. Mail is deleted
  automatically after the retention period, but you have only the
  operators' word for how the server is run.
- It is one more place your codes could leak from if it is ever broken
  into.
- It runs the code in this repository, but you cannot check that from
  outside.

- If you let it look in another mailbox of yours, it holds that mailbox's
  password. Its operators could then read everything in that mailbox, not
  only the codes.

The same goes for any Codemail server someone else runs for you. Use a
hosted server only if you trust the people behind it, and prefer your own
server for accounts that matter: banking, your main email, anything that
can reset other passwords.

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

Codemail only receives. Unless something else sends mail from the domain,
also publish records that tell other mail servers to reject anything
claiming to come from it; otherwise anyone can forge your addresses:

| Type | Name                     | Value                                             | Why |
|------|--------------------------|---------------------------------------------------|-----|
| TXT  | example.com              | `v=spf1 -all`                                     | No server may send as this domain |
| TXT  | *.example.com            | `v=spf1 -all`                                     | The same for `you.example.com`; SPF is not inherited |
| TXT  | _dmarc.example.com       | `v=DMARC1; p=reject; sp=reject; adkim=s; aspf=s`  | Reject what fails, on the domain and every subdomain |
| TXT  | *._domainkey.example.com | `v=DKIM1; p=`                                     | No valid signature exists |

The web app lists the same records, with the right names, for every
domain you or your users add. If you do send mail from the domain, keep
your existing SPF, DKIM and DMARC records instead.

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

## Codes from a mailbox you already have

Many accounts are already tied to an address you cannot change. Under
Addresses, "Your other mailboxes" links such a mailbox in one of two
ways. Neither needs anything from this server's administrator.

**Forwarding.** Link the mailbox's address, then set up a rule or
automatic forwarding in that mailbox to one of your Codemail addresses.
Each linked mailbox has a switch, on by default, to keep only messages
with a code or link and drop the rest, so a forward-everything rule does
not copy your whole inbox. Turn it off if you forward only selected mail
and want all of it kept. Messages you forward by hand work too: the
original sender is read from the quoted header.

**Polling.** Give the server the mailbox's IMAP or JMAP address and an
app password or API token. It signs in, looks in the inbox and the junk
folder every 20 seconds (and at once when the extension is waiting for a
code), and copies only new messages that carry a code or link. Folders
are opened read-only and nothing is marked as read. Mail from before the
mailbox was linked is not touched.

- Use an app password or token you can revoke. The server stores it
  encrypted with a key kept in `fetch.key` in the data directory, but it
  has to be able to use it, so whoever runs the server can too.
- Outlook.com and Microsoft 365 no longer accept passwords for IMAP. Use
  forwarding for those.
- A hosted server refuses mailbox servers on private addresses. If your
  mail server is on your own network, start Codemail with
  `-fetch-allow-private`. `-fetch-interval` changes how often it looks.

**Is the sender still verified?** Mail that comes through another mailbox
no longer arrives from the sender's own server, so the usual check cannot
be made. Codemail verifies the sender's DKIM signature itself and, failing
that, uses the verdict your mailbox provider recorded when it received
the message, if that record is where the provider would have put it. If
neither vouches for the sender, the message is still shown but marked
"Sender not verified", and the extension will not offer its code on a
website by itself.

## Backup and upgrade

All state is in the data directory. To back up while the server is
running:

    ./codemail backup -data /var/lib/codemail /backups/codemail-$(date +%F).db

writes a consistent copy of the database. If anyone has linked a mailbox
for polling, also keep a copy of `fetch.key` from the data directory:
without it the stored mailbox passwords cannot be read and have to be
entered again. To upgrade, replace the binary; the database is migrated on
start.

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
    e2e/extension.sh     # tests the browser extension in Chromium
    e2e/firefox.sh       # the same in an installed Firefox

The web app in `internal/web/static` is plain HTML, CSS and JavaScript
modules with no build step. `DESIGN.md` describes the architecture and
`docs/API.md` is the API reference.

## How it was built

Codemail was co-coded with Claude Fable 5.2, Anthropic's AI model, working
in Claude Code. The maintainer set the requirements, made the design and
release decisions and tried the result; Fable wrote the server, the apps
and the extension, wrote and ran the tests, and checked the extension in
Chromium, Firefox and Safari. Bug reports and corrections are welcome.

Codemail itself uses no AI service when it runs. The part that finds
codes in messages is a small statistical scorer that ships inside the
binary, trained on labelled mail.

## Licence

Codemail is free software, released under the GNU Affero General Public
License, version 3. See `LICENSE`. If you run a modified version as a
service, you must offer its source to the people who use it.
