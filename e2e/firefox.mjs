// Tests the browser extension in a real Firefox against a dev server
// started with:  codemail serve -dev -dev-trust-senders
//   node firefox.mjs <setup-url>
// Firefox is driven through geckodriver, which Selenium fetches on first
// use. Set FIREFOX_BIN if Firefox is not in the usual place.
import { Builder, By, until, Origin } from 'selenium-webdriver';
import firefox from 'selenium-webdriver/firefox.js';
import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';

const [setupURL] = process.argv.slice(2);
const origin = new URL(setupURL).origin;
const version = JSON.parse(fs.readFileSync('../extension/manifest.json', 'utf8')).version;
const uuid = '6f1c2b1e-3c0a-4a53-9d6e-0c5a6f6f0001';
const popupURL = `moz-extension://${uuid}/popup.html`;

// Stand-in websites with code fields, reached by name through Firefox's
// own "these names are this machine" setting.
const sitePort = 8099;
// E2E_USER and E2E_WINDOW (WIDTHxHEIGHT) exist so the screenshots can be
// retaken for the add-on listing: see extension/amo/README.md.
const user = process.env.E2E_USER || 'kaity';
const [winW, winH] = (process.env.E2E_WINDOW || '900x700').split('x').map(Number);
const site = (title, field) => `<!doctype html><meta charset="utf-8"><title>${title}</title>
  <body style="font:16px system-ui;padding:40px"><h1>${title}</h1><p>Enter the code we emailed you.</p>${field}</body>`;
const single = '<label>Verification code <input id="code" autocomplete="one-time-code" style="font-size:18px;padding:8px"></label>';
const boxes = '<form><div aria-label="Verification code">' + Array.from({ length: 6 }, (_, i) => `<input class="b" maxlength="1" inputmode="numeric" aria-label="Digit ${i + 1}" style="width:36px;font-size:20px;text-align:center;margin-right:6px">`).join('') + '</div></form>';
const sites = http.createServer((req, res) => {
  const evil = (req.headers.host || '').startsWith('evil.');
  res.setHeader('Content-Type', 'text/html');
  res.end(site(evil ? 'Totally Northwind' : 'Northwind Bank', req.url.includes('boxes') ? boxes : single));
}).listen(sitePort, '127.0.0.1');
const bank = `http://login.northwind.example:${sitePort}`;
const evil = `http://evil.example:${sitePort}`;

const options = new firefox.Options()
  .addArguments('-headless')
  .setPreference('network.dns.localDomains', 'login.northwind.example,evil.example')
  .setPreference('extensions.webextensions.uuids', JSON.stringify({ 'codemail@tikaydaes.github.io': uuid }))
  // The permission question Firefox shows when the extension asks to reach
  // the server cannot be clicked from here, so it is answered "yes".
  .setPreference('extensions.webextOptionalPermissionPrompts', false)
  .setPreference('dom.security.https_only_mode', false)
  .setPreference('dom.security.https_first', false);
const bin = process.env.FIREFOX_BIN || (process.platform === 'darwin' ? '/Applications/Firefox.app/Contents/MacOS/firefox' : '');
if (bin) options.setBinary(bin);
const service = new firefox.ServiceBuilder().addArguments('--allow-system-access');
if (process.env.DEBUG_FIREFOX) service.setStdio('inherit').addArguments('--log', 'debug');
const driver = await new Builder().forBrowser('firefox').setFirefoxOptions(options).setFirefoxService(service).build();
await driver.installAddon(path.resolve(`../extension/dist/codemail-firefox-${version}.zip`), true);

await driver.manage().window().setRect({ width: winW, height: winH });
fs.mkdirSync('shots', { recursive: true });
let failed = 0;
const step = async (name, fn) => {
  try { await fn(); console.log('ok  ', name); } catch (e) {
    failed++; console.log('FAIL', name, '-', String(e.message).split('\n')[0]);
    console.log('     page shows:', await bodyText().catch(() => '?'));
  }
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const shot = async (name) => fs.writeFileSync(`shots/${name}.png`, await driver.takeScreenshot(), 'base64');
const mail = (from, subject, body, html = '') => execFileSync('python3', ['-c', `
import smtplib, sys
from email.message import EmailMessage
m=EmailMessage(); m['From']=sys.argv[1]; m['To']='${user}@codes.example'; m['Subject']=sys.argv[2]; m.set_content(sys.argv[3])
if sys.argv[4]: m.add_alternative(sys.argv[4], subtype='html')
s=smtplib.SMTP('127.0.0.1',2525); s.send_message(m); s.quit()`, from, subject, body, html]);
const code = (n) => mail('Northwind Bank <otp@northwind.example>', 'Your one-time passcode', `Use ${n} to finish signing in. It expires in 10 minutes.`);

const q = (s) => `"${s}"`;
const find = (xpath, ms = 10000) => driver.wait(until.elementLocated(By.xpath(xpath)), ms).then((el) => driver.wait(until.elementIsVisible(el), ms));
const text = (t, ms) => find(`//*[text()[contains(normalize-space(), ${q(t)})]]`, ms);
const button = (t) => find(`//button[normalize-space()=${q(t)}]`);
const field = (label) => find(`//input[@id=//label[contains(normalize-space(), ${q(label)})]/@for] | //label[contains(normalize-space(), ${q(label)})]//input | //input[@aria-label=${q(label)}]`);
const bodyText = async () => (await driver.findElement(By.css('body')).getText()).replace(/\n+/g, ' | ').slice(0, 300);
const api = async (p, init = {}) => fetch(origin + '/api/v1' + p, { ...init, headers: { Authorization: 'Bearer ' + tokens.access, 'Content-Type': 'application/json' } });
const stored = () => driver.executeAsyncScript('const done = arguments[arguments.length - 1]; browser.storage.local.get(null).then((v) => done(JSON.parse(JSON.stringify(v))), (e) => done({ error: String(e) }));');
// The offer sits just under the focused field in a closed shadow root, so
// it can only be reached by position.
const clickOffer = async (css) => {
  const r = await driver.findElement(By.css(css)).getRect();
  await driver.actions().move({ origin: Origin.VIEWPORT, x: Math.round(r.x + 60), y: Math.round(r.y + r.height + 30) }).click().perform();
};
const focus = (css) => driver.executeScript('document.querySelector(arguments[0]).focus()', css);

const createAccount = async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const cdp = await ctx.newCDPSession(page);
  await cdp.send('WebAuthn.enable');
  await cdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
  await page.goto(setupURL);
  await page.getByLabel('Username').fill(user);
  await page.getByRole('button', { name: 'Create passkey' }).click();
  await page.getByLabel('I have saved these codes').check();
  await page.getByRole('button', { name: 'Continue' }).click();
  await page.getByRole('link', { name: 'Admin' }).click();
  await page.getByLabel('Domain', { exact: true }).fill('codes.example');
  await page.getByRole('button', { name: 'Add shared domain' }).click();
  await page.getByText('Claim and generate addresses on codes.example').waitFor();
  // A username only signs the user in; mail needs an address they claim.
  await page.getByRole('link', { name: 'Addresses' }).click();
  await page.getByRole('button', { name: 'Claim address' }).click();
  await page.getByText(`${user}@codes.example`).first().waitFor();
  const cookie = (await ctx.cookies(origin))[0];
  await browser.close();
  return cookie;
};

// The driver may not navigate to extension pages itself, so the popup is
// opened as a tab from Firefox's own (privileged) side.
const openPopup = async () => {
  const before = await driver.getAllWindowHandles();
  await driver.setContext(firefox.Context.CHROME);
  await driver.executeScript(`const w = Services.wm.getMostRecentWindow('navigator:browser');
    w.gBrowser.selectedTab = w.gBrowser.addTab(arguments[0], { triggeringPrincipal: Services.scriptSecurityManager.getSystemPrincipal() });`, popupURL);
  await driver.setContext(firefox.Context.CONTENT);
  let tab;
  for (let i = 0; i < 50 && !tab; i++) {
    await sleep(100);
    tab = (await driver.getAllWindowHandles()).find((h) => !before.includes(h));
  }
  await driver.switchTo().window(tab);
  return tab;
};

let tokens = {};
let main;
await step('create account and connect the extension', async () => {
  main = await driver.getWindowHandle();
  // Firefox's simulated passkey device refuses to create passkeys, so the
  // account is created in Chromium and its session handed to Firefox. The
  // extension's own sign-in still happens entirely in Firefox.
  const session = await createAccount();
  await driver.get(origin);
  await driver.manage().addCookie({ name: session.name, value: session.value, path: '/', httpOnly: true, sameSite: 'Strict' });
  await driver.get(origin);
  await find('//a[normalize-space()="Admin"]');

  const popup = await openPopup();
  const address = await field('Server address');
  await address.clear();
  await address.sendKeys(origin);
  const before = await driver.getAllWindowHandles();
  await (await button('Connect')).click();
  let tab;
  for (let i = 0; i < 50 && !tab; i++) {
    await sleep(200);
    tab = (await driver.getAllWindowHandles()).find((h) => !before.includes(h));
  }
  if (!tab) throw new Error('no sign-in tab opened; popup says: ' + await bodyText());
  await driver.switchTo().window(tab);
  await text('Connect a device').catch(async () => { throw new Error('approval page shows: ' + await bodyText()); });
  await text('Firefox extension');
  await shot('fx-0-approve');
  await (await button('Allow')).click();
  for (let i = 0; i < 50 && (await driver.getAllWindowHandles()).includes(tab); i++) await sleep(200);
  if ((await driver.getAllWindowHandles()).includes(tab)) throw new Error('the sign-in tab was not closed');
  await driver.switchTo().window(popup);
  await driver.navigate().refresh();
  await text(user);
  await button('Disconnect');
  tokens = await stored();
  if (!tokens.access || tokens.pending) throw new Error('the extension did not finish connecting: ' + JSON.stringify(tokens).slice(0, 200));
  await driver.close();
  await driver.switchTo().window(main);
});

await step('offers and fills a code on the sender\'s own site', async () => {
  await driver.get(bank + '/verify');
  await focus('#code');
  await sleep(800);
  code('834201');
  await sleep(3500);
  await shot('fx-1-offer');
  const leaked = await driver.executeScript('return { root: document.querySelector("codemail-offer")?.shadowRoot ? 1 : 0, text: document.documentElement.innerText }');
  if (leaked.root || leaked.text.includes('834')) throw new Error('the page can read the offered code');
  await clickOffer('#code');
  const value = await driver.findElement(By.css('#code')).getAttribute('value');
  if (value !== '834201') throw new Error(`field holds "${value}"`);
  await sleep(500);
  const list = await (await api('/messages?kind=code')).json();
  if (!list.messages[0].used_at) throw new Error('the code was not marked used');
});

await step('fills a field made of six single-digit boxes', async () => {
  code('482913');
  await driver.get(bank + '/boxes');
  await focus('.b');
  await sleep(3500);
  await shot('fx-2-boxes');
  await clickOffer('.b');
  const value = await driver.executeScript('return [...document.querySelectorAll(".b")].map((e) => e.value).join("")');
  if (value !== '482913') throw new Error(`boxes hold "${value}"`);
});

await step('does not offer the code on an unrelated site', async () => {
  code('111222');
  await driver.get(evil + '/verify');
  await focus('#code');
  await sleep(3500);
  await shot('fx-3-other-site');
  await clickOffer('#code');
  const value = await driver.findElement(By.css('#code')).getAttribute('value');
  if (value !== '') throw new Error(`the code was filled on another site: "${value}"`);
});

await step('offers a confirmation link and opens it', async () => {
  mail('Northwind Bank <hello@northwind.example>', 'Confirm your email address', 'Confirm your email',
    `<p>Confirm your email address to finish.</p><a href="${bank}/confirm?token=8f3a9c1e7b2d4f6a8c0e1b3d5f7a9c1e">Verify email</a>`);
  const list = await (await api('/messages?kind=code')).json();
  for (const m of list.messages.filter((m) => m.code && !m.used_at)) {
    await api('/messages/' + m.id, { method: 'PATCH', body: JSON.stringify({ used: true }) });
  }
  await driver.get(bank + '/verify');
  await focus('#code');
  await sleep(3500);
  await shot('fx-4-link');
  const before = await driver.getAllWindowHandles();
  await clickOffer('#code');
  let tab;
  for (let i = 0; i < 25 && !tab; i++) {
    await sleep(200);
    tab = (await driver.getAllWindowHandles()).find((h) => !before.includes(h));
  }
  if (!tab) throw new Error('the link did not open in a new tab');
  await driver.switchTo().window(tab);
  const opened = await driver.getCurrentUrl();
  await driver.close();
  await driver.switchTo().window(main);
  if (!opened.includes('/confirm?token=8f3a')) throw new Error('opened ' + opened);
  await sleep(500);
  const after = await (await api('/messages?kind=code')).json();
  if (!after.messages.find((m) => m.link)?.used_at) throw new Error('the link was not recorded as used');
});

await step('popup lists recent codes, shows the badge, and disconnects', async () => {
  code('111222');
  await driver.get(bank + '/verify');
  await focus('#code');
  await sleep(3500);
  await openPopup();
  await text('111 222', 5000).catch(async () => { throw new Error('popup shows: ' + await bodyText()); });
  await text('Northwind Bank');
  const badge = await driver.executeAsyncScript('const done = arguments[arguments.length - 1]; browser.action.getBadgeText({}).then(done, (e) => done("error " + e));');
  if (badge !== '1') throw new Error('badge shows "' + badge + '", want 1');
  await driver.manage().window().setRect({ width: 380, height: 640 });
  await shot('fx-5-popup');
  await (await button('Disconnect')).click();
  await text('Connect to your Codemail server.', 5000);
  const res = await api('/me');
  if (res.status !== 401) throw new Error('tokens still work after Disconnect');
});

await driver.quit();
sites.close();
process.exit(failed ? 1 : 0);
