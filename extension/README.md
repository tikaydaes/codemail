# Codemail browser extension

Offers sign-in codes from your own Codemail server in the field that asks
for them, and gives you recent codes and per-site addresses from the
toolbar.

One codebase for Chromium browsers (Chrome, Edge, Brave, Vivaldi, Opera)
and Firefox. It is not published in the Chrome Web Store or on
addons.mozilla.org.

## What it does

- When you focus a verification-code field, it waits for the email and
  offers the code under the field. Click Fill and it is typed in,
  including into fields made of six separate boxes.
- A code is only offered on a site when the sender was verified and the
  sender's domain is that site's domain, or you have linked that sender to
  the site from the popup. Anything else stays in the popup. This is what
  stops a look-alike site from collecting your code.
- The offer is drawn where page scripts cannot read it. The page only
  receives the code when you click Fill.
- The popup lists recent codes and links, and can generate a new address
  for the site you are on.

## Privacy

The extension talks only to the Codemail server you connect it to, and
asks the browser for permission to reach that one address. When you focus
a code field, it sends that site's hostname to your server to look for a
matching code. It sends nothing else, and nothing to anyone else.

## Build

    node build.mjs

produces `dist/chrome`, `dist/firefox` and a zip of each.

## Install

**Chromium browsers.** Outside the Web Store, these only load extensions
in developer mode: open `chrome://extensions`, turn on Developer mode,
choose Load unpacked and pick `dist/chrome`. The browser may remind you
on start-up that a developer-mode extension is active.

**Firefox.** For a quick try, open `about:debugging`, choose This Firefox,
then Load Temporary Add-on and pick `dist/firefox/manifest.json`; it is
removed when Firefox closes. Release versions of Firefox only keep
extensions that Mozilla has signed. Signing does not require listing in
their store: with a free addons.mozilla.org account you can sign your own
build as "unlisted" and install the resulting file anywhere:

    npx web-ext sign -s dist/firefox --channel unlisted --api-key ... --api-secret ...

Firefox Developer Edition, Nightly and ESR can instead turn off the
signature requirement (`xpinstall.signatures.required` in `about:config`).

**Safari.** Not supported yet. Safari extensions must be wrapped in an app
with Xcode, and Safari lacks the sign-in API this extension uses to
connect, so that step needs a Safari-specific replacement.

## Connect

Click the toolbar icon, enter your server's address and choose Connect.
Your server's sign-in page opens; sign in with your passkey and approve
the extension. It then appears under Devices in the web app, where you can
disconnect it.

## Test

`e2e/extension.mjs` loads the extension into Chromium against a local
server and checks filling, six-box fields, the unrelated-site refusal and
the popup. See the comment at the top of that file.
