import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
const [setupURL] = process.argv.slice(2);
const origin = new URL(setupURL).origin;
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1280, height: 860 } });
await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin });
const page = await ctx.newPage();
const errors = [];
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
page.on('pageerror', (e) => errors.push('PAGEERROR ' + e.message));
page.on('dialog', (d) => d.accept());
const cdp = await ctx.newCDPSession(page);
await cdp.send('WebAuthn.enable');
await cdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
const shot = (n) => page.screenshot({ path: `shots/${n}.png`, fullPage: true });
const step = async (name, fn) => { try { await fn(); console.log('ok  ', name); } catch (e) { console.log('FAIL', name, '-', e.message.split('\n')[0]); await shot('fail-' + name.replace(/\W+/g, '-')); } };
const mail = (to, from, subject, body) => execFileSync('python3', ['-c', `
import smtplib, sys
from email.message import EmailMessage
m=EmailMessage(); m['From']=sys.argv[2]; m['To']=sys.argv[1]; m['Subject']=sys.argv[3]
m.set_content(sys.argv[4])
if len(sys.argv)>5: m.add_alternative(sys.argv[5], subtype='html')
s=smtplib.SMTP('127.0.0.1',2525); s.send_message(m); s.quit()`, to, from, subject, ...body]);

await step('setup page', async () => {
  await page.goto(setupURL);
  await page.getByRole('heading', { name: 'Set up this server' }).waitFor({ timeout: 5000 });
  await shot('01-setup');
});
await step('create admin with passkey', async () => {
  await page.getByLabel('Username').fill('kaity');
  await page.getByRole('button', { name: 'Create passkey' }).click();
  await page.getByRole('heading', { name: 'Save your recovery codes' }).waitFor({ timeout: 5000 });
  await shot('02-recovery');
  await page.getByLabel('I have saved these codes').check();
  await page.getByRole('button', { name: 'Continue' }).click();
  await page.getByRole('heading', { name: 'Codes', exact: true }).waitFor({ timeout: 5000 });
});
await step('admin adds shared domain', async () => {
  await page.getByRole('link', { name: 'Admin' }).click();
  await page.getByRole('heading', { name: 'Admin', exact: true }).waitFor();
  await page.getByLabel('Domain', { exact: true }).fill('codes.example');
  await page.getByRole('button', { name: 'Add shared domain' }).click();
  await page.getByText('username@codes.example, with +tags').waitFor({ timeout: 5000 });
  await shot('03-admin');
});
await step('code arrives live', async () => {
  await page.getByRole('link', { name: 'Codes' }).click();
  await page.getByText('No codes yet').waitFor();
  await page.getByText('Live', { exact: true }).waitFor({ timeout: 5000 });
  mail('kaity+bank@codes.example', 'Northwind Bank <otp@northwind.example>', 'Your one-time passcode', ['Use 834201 to finish signing in. Never share this code. It expires in 10 minutes.']);
  await page.locator('.code-big', { hasText: '834 201' }).waitFor({ timeout: 5000 });
  mail('anything@kaity.codes.example', 'Acme Cloud <hello@acmecloud.example>', 'Confirm your email address',
    ['Confirm: https://app.acmecloud.example/verify?token=8f3a9c1e7b2d4f6a8c0e1b3d5f7a9c1e', '<p>Hi Kaity,</p><p>Confirm your email address to finish.</p><a href="https://app.acmecloud.example/verify?token=8f3a9c1e7b2d4f6a8c0e1b3d5f7a9c1e">Verify email</a><p><img src="https://acmecloud.example/pixel.png"></p>']);
  mail('kaity@codes.example', 'News <news@shop.example>', 'November news', ['We shipped 12 features this month.']);
  await page.getByText('Link to app.acmecloud.example').waitFor({ timeout: 5000 });
});
await step('message detail and copy', async () => {
  await page.getByRole('button', { name: 'Details' }).click();
  await page.getByText('Sender check').waitFor({ timeout: 5000 });
  await shot('04-codes');
  await page.getByRole('button', { name: 'Copy code' }).first().click();
  await page.waitForFunction(async () => (await navigator.clipboard.readText()) === '834201', null, { timeout: 5000 });
  await page.getByText('· used').waitFor({ timeout: 5000 });
});
await step('message body is rendered from Markdown', async () => {
  await page.locator('.item', { hasText: 'Acme Cloud' }).getByRole('button').first().click();
  const link = page.locator('.md a', { hasText: 'Verify email' });
  await link.waitFor({ timeout: 5000 });
  const href = await link.getAttribute('href');
  if (!href.startsWith('https://app.acmecloud.example/verify?token=')) throw new Error('bad href ' + href);
  if (await page.locator('.aside iframe, .aside img').count()) throw new Error('message body contains a frame or image');
  await shot('04b-markdown');
  // The renderer must treat hostile content as text.
  const probe = await page.evaluate(async () => {
    const { renderMarkdown } = await import('/md.js');
    const el = renderMarkdown('# Title\n\nHello **bold** and *em* `code`  \nnext line\n\n<img src=x onerror=alert(1)> <script>alert(2)</script>\n\n[bad](javascript:alert(3)) [ok](https://ok.example/a "t") <https://auto.example/>\n\n- one\n- two\n  - nested\n\n1. first\n2. second\n\n> quoted\n\n* * *\n\n\\*not em\\*');
    return { html: el.innerHTML, tags: [...el.querySelectorAll('*')].map((e) => e.tagName).join(' '),
      hrefs: [...el.querySelectorAll('a')].map((a) => a.getAttribute('href')) };
  });
  for (const bad of ['IMG', 'SCRIPT']) if (probe.tags.split(' ').includes(bad)) throw new Error('rendered a ' + bad + ' element');
  if (probe.hrefs.some((x) => !/^https:/.test(x)) || probe.hrefs.length !== 2) throw new Error('bad links ' + JSON.stringify(probe.hrefs));
  for (const want of ['<h3>Title</h3>', '<strong>bold</strong>', '<em>em</em>', '<code>code</code>', '<br>', '<ul><li>one</li><li>two<ul><li>nested</li></ul></li></ul>', '<ol><li>first</li><li>second</li></ol>', '<blockquote><p>quoted</p></blockquote>', '<hr>', '*not em*', '&lt;img src=x'])
    if (!probe.html.includes(want)) throw new Error('missing ' + want + ' in ' + probe.html);
  await page.locator('.item', { hasText: 'Northwind Bank' }).getByRole('button').first().click();
  await page.getByText('Sender check').waitFor({ timeout: 5000 });
});
await step('wrong code dialog', async () => {
  await page.getByRole('button', { name: 'Wrong code?' }).click();
  await page.getByRole('heading', { name: 'Correct this message' }).waitFor({ timeout: 5000 });
  await shot('05-correction');
  await page.getByLabel('There is no sign-in code').check();
  await page.getByRole('button', { name: 'Save' }).click();
  await page.locator('.item', { hasText: 'Northwind Bank' }).waitFor({ state: 'detached', timeout: 5000 });
});
await step('other mail', async () => {
  await page.getByRole('link', { name: 'Other mail' }).click();
  await page.locator('.item', { hasText: 'November news' }).waitFor({ timeout: 5000 });
  await page.locator('.item', { hasText: 'Northwind Bank' }).waitFor({ timeout: 5000 });
});
await step('addresses: generate and claim', async () => {
  await page.getByRole('link', { name: 'Addresses' }).click();
  await page.getByText('kaity+anything@codes.example').waitFor({ timeout: 5000 });
  await page.getByLabel('Service name').fill('Northwind Bank');
  await page.getByRole('button', { name: 'Generate address' }).click();
  await page.getByText('Generated · no mail yet').waitFor({ timeout: 5000 });
  await page.getByLabel('Name', { exact: true }).fill('shop');
  await page.getByRole('button', { name: 'Claim address' }).click();
  await page.getByText('shop@codes.example').waitFor({ timeout: 5000 });
  await page.getByLabel('Domain', { exact: true }).last().fill('kaity.example');
  await page.getByRole('button', { name: 'Add a domain' }).click();
  await page.getByText('Waiting for DNS').waitFor({ timeout: 5000 });
  await shot('06-addresses');
});
await step('devices: QR pairing', async () => {
  await page.getByRole('link', { name: 'Devices' }).click();
  await page.getByRole('button', { name: 'Add device' }).click();
  const img = page.getByAltText('Pairing QR code');
  await img.waitFor({ timeout: 5000 });
  await page.waitForFunction(() => document.querySelector('img.qr').naturalWidth > 0, null, { timeout: 5000 });
  await shot('07-devices');
});
await step('oauth approval page', async () => {
  await page.goto(origin + '/oauth/authorize?response_type=code&client_id=desktop&redirect_uri=' + encodeURIComponent('http://127.0.0.1:9/cb') + '&code_challenge=' + 'a'.repeat(43) + '&code_challenge_method=S256&state=s1&device_name=Test%20Mac&platform=macos');
  await page.getByRole('heading', { name: 'Connect a device' }).waitFor({ timeout: 5000 });
  await shot('08-oauth');
  const nav = page.waitForRequest((r) => r.url().startsWith('http://127.0.0.1:9/cb?'), { timeout: 5000 });
  await page.getByRole('button', { name: 'Allow' }).click();
  const req = await nav;
  if (!/code=[\w-]{20,}/.test(req.url()) || !req.url().includes('state=s1')) throw new Error('bad redirect ' + req.url());
});
await step('settings, sign out, sign in with passkey', async () => {
  await page.waitForTimeout(800);
  await page.goto('about:blank').catch(() => {});
  await page.waitForTimeout(300);
  await page.goto(origin + '/#/settings');
  await page.getByRole('heading', { name: 'Passkeys' }).waitFor({ timeout: 5000 });
  await page.getByRole('heading', { name: 'Signed-in browsers' }).waitFor();
  await page.getByText('This browser').waitFor();
  await page.getByText(/Chrome on (macOS|Linux|Windows)/).first().waitFor();
  await shot('09-settings');
  await page.getByRole('button', { name: 'Sign out' }).click();
  await page.getByRole('button', { name: 'Sign in with a passkey' }).waitFor({ timeout: 5000 });
  await shot('10-signin');
  await page.getByRole('button', { name: 'Sign in with a passkey' }).click();
  await page.getByText('Signed in as').waitFor({ timeout: 5000 });
});
await step('phone width', async () => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(origin + '/#/codes');
  await page.getByText('Link to app.acmecloud.example').waitFor({ timeout: 5000 });
  await shot('11-phone');
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) throw new Error('page scrolls sideways by ' + overflow + 'px');
});
console.log('console errors:', errors.length ? errors : 'none');
await browser.close();
