# Codemail extension privacy policy

The Codemail extension connects your browser to one Codemail server of
your choosing: your own, one run by someone you trust, or the public
server at codegrab.me that it offers by default. It talks to nothing else.

**What it sends, and where.** The extension communicates only with the
Codemail server whose address you enter when you connect it. To that
server it sends:

- the hostname of a website (for example `login.example.com`) when you
  focus a verification-code field there, or open the popup on it, so the
  server can find a code from that site's sender;
- the label for a new address, which is that hostname, when you ask the
  popup to make an address for the current site;
- which code or link you used, so it can be marked as used;
- the sign-in tokens that identify this browser to your server.

**What it does not do.** It does not send page content, form contents,
passwords, browsing history or anything else to your server. It sends
nothing at all to the extension's developers, to Mozilla, or to any third
party. It contains no analytics, advertising or tracking.

**What it stores.** Your server's address and this browser's sign-in
tokens are kept in the extension's local storage in your browser.
Disconnecting from the popup deletes them and removes this browser from
your server's device list.

**Your server.** Whoever runs the Codemail server you connect to can read
the mail it receives for you, including sign-in codes. What it keeps, and
for how long, is decided by its operator. Use a server you trust, or run
your own: https://github.com/tikaydaes/codemail

Questions and reports: https://github.com/tikaydaes/codemail/issues
