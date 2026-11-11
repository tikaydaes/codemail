// Drives app/tool/live_check.dart against a running dev server: creates the
// admin with a virtual passkey, approves the app in the browser, and sends
// the email the app waits for.
import { chromium } from 'playwright';
import { spawn, execFileSync } from 'node:child_process';
const [setupURL] = process.argv.slice(2);
const origin = new URL(setupURL).origin;
const browser = await chromium.launch();
const ctx = await browser.newContext();
const page = await ctx.newPage();
const cdp = await ctx.newCDPSession(page);
await cdp.send('WebAuthn.enable');
await cdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
await page.goto(setupURL);
await page.getByLabel('Username').fill('kaity');
await page.getByRole('button', { name: 'Create passkey' }).click();
await page.getByLabel('I have saved these codes').check();
await page.getByRole('button', { name: 'Continue' }).click();
await page.getByRole('link', { name: 'Admin' }).click();
await page.getByLabel('Domain', { exact: true }).fill('codes.example');
await page.getByRole('button', { name: 'Add shared domain' }).click();
await page.getByText('username@codes.example, with +tags').waitFor();

const dart = spawn('dart', ['run', 'tool/live_check.dart', origin], { cwd: '../app' });
let out = '';
const seen = (re) => new Promise((resolve, reject) => {
  const t = setInterval(() => { const m = out.match(re); if (m) { clearInterval(t); resolve(m); } }, 100);
  setTimeout(() => { clearInterval(t); reject(new Error('timed out waiting for ' + re + '\n' + out)); }, 90000);
});
dart.stdout.on('data', (d) => { out += d; });
dart.stderr.on('data', (d) => { out += d; });
const [, url] = await seen(/AUTHORIZE (\S+)/);
await page.goto(url);
await page.getByText('Live check').waitFor();
await page.getByRole('button', { name: 'Allow' }).click();
await seen(/STREAM open/);
execFileSync('python3', ['-c', `
import smtplib
from email.message import EmailMessage
m=EmailMessage(); m['From']='Northwind Bank <otp@northwind.example>'; m['To']='kaity@codes.example'; m['Subject']='Your one-time passcode'
m.set_content('Use 834201 to finish signing in. It expires in 10 minutes.')
s=smtplib.SMTP('127.0.0.1',2525); s.send_message(m); s.quit()`]);
await seen(/DONE|Unhandled|Error/);
console.log(out.replace(/AUTHORIZE \S+/, 'AUTHORIZE <url>').trim());
await browser.close();
dart.kill();
process.exit(/DONE/.test(out) ? 0 : 1);
