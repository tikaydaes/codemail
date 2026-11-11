// Packages the extension for Chromium browsers and for Firefox.
//
//   node build.mjs          -> dist/chrome, dist/firefox and a zip of each
//   node build.mjs --dev    -> also allows http://localhost, for testing
//
// The two differ only in the manifest: Chromium wants a service worker,
// Firefox wants a background script and an add-on ID.
import fs from 'node:fs';
import { execFileSync } from 'node:child_process';

const dev = process.argv.includes('--dev');
const base = JSON.parse(fs.readFileSync('manifest.json', 'utf8'));
if (dev) base.host_permissions = ['http://localhost/*'];

const targets = {
  chrome: base,
  firefox: {
    ...base,
    background: { scripts: ['background.js'], type: 'module' },
    browser_specific_settings: {
      gecko: {
        id: 'codemail@tikaydaes.github.io',
        strict_min_version: '140.0', // first version with the declaration below
        // Firefox asks extensions to declare what leaves the browser. When a
        // code field is focused, the site's hostname is sent to the user's
        // own Codemail server to find the matching code. Nothing goes
        // anywhere else.
        data_collection_permissions: { required: ['browsingActivity'] },
      },
      gecko_android: { strict_min_version: '142.0' },
    },
  },
};

fs.rmSync('dist', { recursive: true, force: true });
for (const [name, manifest] of Object.entries(targets)) {
  const out = `dist/${name}`;
  fs.mkdirSync(out, { recursive: true });
  fs.cpSync('src', out, { recursive: true });
  fs.cpSync('icons', `${out}/icons`, { recursive: true });
  fs.writeFileSync(`${out}/manifest.json`, JSON.stringify(manifest, null, 2) + '\n');
  execFileSync('zip', ['-qr', `../codemail-${name}-${manifest.version}.zip`, '.'], { cwd: out });
  console.log(`built ${out} and dist/codemail-${name}-${manifest.version}.zip`);
}
