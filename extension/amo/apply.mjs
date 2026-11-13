// Applies the store listing in this directory to the add-on's record at
// addons.mozilla.org: the texts in metadata.json, the privacy policy, the
// icon and the screenshots. Submitting a version is done separately, by
// `web-ext sign` (see ../README.md). Safe to run again: it replaces what
// is there.
//
//   WEB_EXT_API_KEY=... WEB_EXT_API_SECRET=... node amo/apply.mjs [--check-license]
//
// Run from extension/. The credentials are the "JWT issuer" and "JWT
// secret" from the developer account's API key page.
import crypto from 'node:crypto';
import fs from 'node:fs';

const id = 'codemail@tikaydaes.github.io';
const base = `https://addons.mozilla.org/api/v5/addons/addon/${id}/`;
const { WEB_EXT_API_KEY: key, WEB_EXT_API_SECRET: secret } = process.env;
if (!key || !secret) throw new Error('set WEB_EXT_API_KEY and WEB_EXT_API_SECRET');

const b64 = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
const token = () => {
  const now = Math.floor(Date.now() / 1000);
  const body = `${b64({ alg: 'HS256', typ: 'JWT' })}.${b64({ iss: key, jti: crypto.randomUUID(), iat: now, exp: now + 60 })}`;
  return `JWT ${body}.${crypto.createHmac('sha256', secret).update(body).digest('base64url')}`;
};
const call = async (method, path, body) => {
  const json = body && !(body instanceof FormData);
  const res = await fetch(base + path, {
    method,
    headers: { Authorization: token(), ...(json ? { 'Content-Type': 'application/json' } : {}) },
    body: json ? JSON.stringify(body) : body,
  });
  const text = await res.text();
  // Edits are rate-limited; the reply says how long to wait.
  const wait = res.status === 429 && /(\d+) seconds/.exec(text);
  if (wait) {
    console.log(`rate-limited, waiting ${wait[1]} s`);
    await new Promise((r) => setTimeout(r, (Number(wait[1]) + 2) * 1000));
    return call(method, path, body);
  }
  if (!res.ok) throw new Error(`${method} ${path || '(add-on)'}: ${res.status} ${text.slice(0, 600)}`);
  return text ? JSON.parse(text) : null;
};
const file = (path) => new Blob([fs.readFileSync(path)], { type: 'image/png' });

const { version, ...listing } = JSON.parse(fs.readFileSync('amo/metadata.json', 'utf8'));

if (process.argv.includes('--check-license')) {
  // The licence belongs to a version; this sets it on the newest one to
  // confirm the server accepts the identifier.
  const { results } = await call('GET', 'versions/?filter=all_with_unlisted');
  const v = await call('PATCH', `versions/${results[0].id}/`, { license: version.license });
  console.log(`version ${v.version}: licence ${v.license?.slug}`);
}

await call('PATCH', '', listing);
console.log('listing texts applied');

await call('PATCH', 'eula_policy/', { privacy_policy: { 'en-US': fs.readFileSync('amo/privacy-policy.md', 'utf8') } });
console.log('privacy policy applied');

const icon = new FormData();
icon.append('icon', file('icons/128.png'), 'icon.png');
await call('PATCH', '', icon);
console.log('icon applied');

const shots = JSON.parse(fs.readFileSync('amo/screenshots/captions.json', 'utf8'));
for (const old of (await call('GET', '')).previews || []) await call('DELETE', `previews/${old.id}/`);
let position = 0;
for (const [name, caption] of Object.entries(shots)) {
  const form = new FormData();
  form.append('image', file(`amo/screenshots/${name}`), name);
  form.append('position', String(position++));
  const made = await call('POST', 'previews/', form);
  await call('PATCH', `previews/${made.id}/`, { caption: { 'en-US': caption } });
  console.log(`screenshot ${name} applied`);
}
