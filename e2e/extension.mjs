// Tests the browser extension in a real Chromium against a dev server
// started with:  codemail serve -dev -dev-trust-senders
//   node extension.mjs <setup-url>
import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const [setupURL] = process.argv.slice(2);
const origin = new URL(setupURL).origin;
const ext = path.resolve('../extension/dist/chrome');
const ctx = await chromium.launchPersistentContext(fs.mkdtempSync(path.join(os.tmpdir(), 'codemail-ext-')), {
  channel: 'chromium', headless: true, viewport: { width: 900, height: 600 },
  args: [`--disable-extensions-except=${ext}`, `--load-extension=${ext}`],
});
const worker = ctx.serviceWorkers()[0] ?? await ctx.waitForEvent('serviceworker');
const extensionId = new URL(worker.url()).host;
fs.mkdirSync('shots', { recursive: true });
let failed = 0;
const step = async (name, fn) => { try { await fn(); console.log('ok  ', name); } catch (e) { failed++; console.log('FAIL', name, '-', e.message.split('\n')[0]); } };
const mail = (from, subject, body) => execFileSync('python3', ['-c', `
import smtplib, sys
from email.message import EmailMessage
m=EmailMessage(); m['From']=sys.argv[1]; m['To']='kaity@codes.example'; m['Subject']=sys.argv[2]; m.set_content(sys.argv[3])
s=smtplib.SMTP('127.0.0.1',2525); s.send_message(m); s.quit()`, from, subject, body]);

// Stand-in websites with code fields.
const site = (title, field) => `<!doctype html><meta charset="utf-8"><title>${title}</title>
  <body style="font:16px system-ui;padding:40px"><h1>${title}</h1><p>Enter the code we emailed you.</p>${field}</body>`;
const single = '<label>Verification code <input id="code" autocomplete="one-time-code" style="font-size:18px;padding:8px"></label>';
const boxes = '<form><div aria-label="Verification code">' + Array.from({ length: 6 }, (_, i) => `<input class="b" maxlength="1" inputmode="numeric" aria-label="Digit ${i + 1}" style="width:36px;font-size:20px;text-align:center;margin-right:6px">`).join('') + '</div></form>';
await ctx.route('https://login.northwind.example/**', (r) => r.fulfill({ contentType: 'text/html', body: site('Northwind Bank', r.request().url().includes('boxes') ? boxes : single) }));
await ctx.route('https://evil.example/**', (r) => r.fulfill({ contentType: 'text/html', body: site('Totally Northwind', single) }));

const page = await ctx.newPage();
const cdp = await ctx.newCDPSession(page);
await cdp.send('WebAuthn.enable');
await cdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });

// Clicks the offer, which sits just under the focused field. It lives in
// a closed shadow root, so it can only be reached by position.
const clickOffer = async (selector) => {
  const box = await page.locator(selector).first().boundingBox();
  await page.mouse.click(box.x + 60, box.y + box.height + 30);
};

let tokens;
await step('create account and connect the extension', async () => {
  await page.goto(setupURL);
  await page.getByLabel('Username').fill('kaity');
  await page.getByRole('button', { name: 'Create passkey' }).click();
  await page.getByLabel('I have saved these codes').check();
  await page.getByRole('button', { name: 'Continue' }).click();
  await page.getByRole('link', { name: 'Admin' }).click();
  await page.getByLabel('Domain', { exact: true }).fill('codes.example');
  await page.getByRole('button', { name: 'Add shared domain' }).click();
  await page.getByText('username@codes.example, with +tags').waitFor();
  // The browser's own sign-in window cannot be driven from a test, so the
  // approval and code exchange are done directly, as the extension would.
  const verifier = crypto.randomBytes(48).toString('base64url');
  const challenge = crypto.createHash('sha256').update(verifier).digest('base64url');
  const redirect = `https://${extensionId}.chromiumapp.org/oauth`;
  const approved = await page.evaluate(async (body) => (await fetch('/api/v1/oauth/approve', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })).json(),
    { client_id: 'codemail-extension', redirect_uri: redirect, code_challenge: challenge, code_challenge_method: 'S256', state: 's', device_name: 'Browser extension', platform: 'browser' });
  const code = new URL(approved.redirect).searchParams.get('code');
  tokens = await (await fetch(origin + '/oauth/token', { method: 'POST', body: new URLSearchParams({ grant_type: 'authorization_code', code, code_verifier: verifier, redirect_uri: redirect }) })).json();
  if (!tokens.access_token) throw new Error('no tokens: ' + JSON.stringify(tokens));
  await worker.evaluate(({ server, t }) => chrome.storage.local.set({ server, access: t.access_token, refresh: t.refresh_token, expires: Date.now() + 3600e3, username: 'kaity' }), { server: origin, t: tokens });
});

await step('offers and fills a code on the sender\'s own site', async () => {
  await page.goto('https://login.northwind.example/verify');
  await page.locator('#code').focus();
  await page.waitForTimeout(800);
  await page.screenshot({ path: 'shots/ext-1-waiting.png' });
  mail('Northwind Bank <otp@northwind.example>', 'Your one-time passcode', 'Use 834201 to finish signing in. It expires in 10 minutes.');
  await page.waitForTimeout(3500);
  await page.screenshot({ path: 'shots/ext-2-offer.png' });
  const leaked = await page.evaluate(() => ({ root: document.querySelector('codemail-offer')?.shadowRoot ?? null, text: document.documentElement.innerText }));
  if (leaked.root !== null || leaked.text.includes('834')) throw new Error('the page can read the offered code');
  await clickOffer('#code');
  const value = await page.locator('#code').inputValue();
  if (value !== '834201') throw new Error(`field holds "${value}"`);
  await page.waitForTimeout(500);
  const list = await (await fetch(origin + '/api/v1/messages?kind=code', { headers: { Authorization: 'Bearer ' + tokens.access_token } })).json();
  if (!list.messages[0].used_at) throw new Error('the code was not marked used');
});

await step('fills a field made of six single-digit boxes', async () => {
  mail('Northwind Bank <otp@northwind.example>', 'Your one-time passcode', 'Use 482913 to finish signing in. It expires in 10 minutes.');
  await page.goto('https://login.northwind.example/boxes');
  await page.locator('.b').first().focus();
  await page.waitForTimeout(3500);
  await page.screenshot({ path: 'shots/ext-3-boxes.png' });
  await clickOffer('.b');
  const value = (await page.locator('.b').evaluateAll((els) => els.map((e) => e.value))).join('');
  if (value !== '482913') throw new Error(`boxes hold "${value}"`);
});

await step('does not offer the code on an unrelated site', async () => {
  mail('Northwind Bank <otp@northwind.example>', 'Your one-time passcode', 'Use 111222 to finish signing in. It expires in 10 minutes.');
  await page.goto('https://evil.example/verify');
  await page.locator('#code').focus();
  await page.waitForTimeout(3500);
  await page.screenshot({ path: 'shots/ext-4-other-site.png' });
  await clickOffer('#code');
  const value = await page.locator('#code').inputValue();
  if (value !== '') throw new Error(`the code was filled on another site: "${value}"`);
});

await step('offers a confirmation link and opens it', async () => {
  execFileSync('python3', ['-c', `
import smtplib
from email.message import EmailMessage
m=EmailMessage(); m['From']='Northwind Bank <hello@northwind.example>'; m['To']='kaity@codes.example'; m['Subject']='Confirm your email address'
m.set_content('Confirm your email')
m.add_alternative('<p>Confirm your email address to finish.</p><a href="https://login.northwind.example/confirm?token=8f3a9c1e7b2d4f6a8c0e1b3d5f7a9c1e">Verify email</a>', subtype='html')
s=smtplib.SMTP('127.0.0.1',2525); s.send_message(m); s.quit()`]);
  // Use up the code from the previous step so the link is what is offered.
  const list = await (await fetch(origin + '/api/v1/messages?kind=code', { headers: { Authorization: 'Bearer ' + tokens.access_token } })).json();
  for (const m of list.messages.filter((m) => m.code && !m.used_at)) {
    await fetch(origin + '/api/v1/messages/' + m.id, { method: 'PATCH', headers: { Authorization: 'Bearer ' + tokens.access_token, 'Content-Type': 'application/json' }, body: JSON.stringify({ used: true }) });
  }
  await page.goto('https://login.northwind.example/verify');
  await page.locator('#code').focus();
  await page.waitForTimeout(3500);
  await page.screenshot({ path: 'shots/ext-4b-link.png' });
  const opened = ctx.waitForEvent('page', { timeout: 5000 });
  await clickOffer('#code');
  const tab = await opened; // the stand-in site is not reachable from this tab, so check the record instead
  await tab.close();
  await page.waitForTimeout(500);
  const after = await (await fetch(origin + '/api/v1/messages?kind=code', { headers: { Authorization: 'Bearer ' + tokens.access_token } })).json();
  const linkMessage = after.messages.find((m) => m.link);
  if (!linkMessage?.link.startsWith('https://login.northwind.example/confirm?token=') || !linkMessage.used_at) throw new Error('the link was not recorded as used');
  const badge = await worker.evaluate(() => chrome.action.getBadgeText({}));
  if (badge !== '') throw new Error('badge still shows "' + badge + '" after everything was used');
  mail('Northwind Bank <otp@northwind.example>', 'Your one-time passcode', 'Use 111222 to finish signing in. It expires in 10 minutes.');
  await page.goto('https://login.northwind.example/verify');
  await page.locator('#code').focus();
  await page.waitForTimeout(3500);
  const badge2 = await worker.evaluate(() => chrome.action.getBadgeText({}));
  if (badge2 !== '1') throw new Error('badge shows "' + badge2 + '", want 1');
});

await step('popup lists recent codes', async () => {
  const popup = await ctx.newPage();
  await popup.setViewportSize({ width: 340, height: 520 });
  await popup.goto(`chrome-extension://${extensionId}/popup.html`);
  await popup.waitForTimeout(1500);
  if (process.env.DEBUG_POPUP) console.log('POPUP:', (await popup.locator('body').innerText()).replace(/\n+/g, ' | '));
  await popup.getByText('111 222').first().waitFor({ timeout: 5000 });
  await popup.getByText('Northwind Bank').first().waitFor();
  await popup.getByRole('button', { name: 'Disconnect' }).waitFor();
  await popup.screenshot({ path: 'shots/ext-5-popup.png' });
  await popup.getByRole('button', { name: 'Disconnect' }).click();
  await popup.getByText('Connect to your Codemail server.').waitFor({ timeout: 5000 });
  await popup.screenshot({ path: 'shots/ext-6-connect.png' });
  const res = await fetch(origin + '/api/v1/me', { headers: { Authorization: 'Bearer ' + tokens.access_token } });
  if (res.status !== 401) throw new Error('tokens still work after Disconnect');
});

await ctx.close();
process.exit(failed ? 1 : 0);
