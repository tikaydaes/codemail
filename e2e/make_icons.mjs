// Derives every icon file from app/assets/icon/source.jpg (the full-bleed
// master artwork). Run from e2e/:  node make_icons.mjs
// It also runs flutter_launcher_icons to produce the platform icon sets.
import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';

const dir = '../app/assets/icon';
const source = 'data:image/jpeg;base64,' + fs.readFileSync(`${dir}/source.jpg`).toString('base64');
const browser = await chromium.launch();
const page = await browser.newPage();
await page.setContent(`<img id="src" src="${source}">`);
await page.waitForFunction(() => document.getElementById('src').complete);

// Each recipe draws onto a square canvas and returns PNG bytes.
const render = (size, recipe) => page.evaluate(async ({ size, recipe }) => {
  const img = document.getElementById('src');
  const c = document.createElement('canvas');
  c.width = c.height = size;
  const g = c.getContext('2d');
  g.imageSmoothingQuality = 'high';
  const squircle = (x, y, s) => { g.beginPath(); g.roundRect(x, y, s, s, s * 0.2237); };
  if (recipe === 'square') {
    // Opaque and full-bleed: iOS and Windows apply their own shape.
    g.drawImage(img, 0, 0, size, size);
  } else if (recipe === 'rounded') {
    // Rounded corners on transparency: Linux, tray, web.
    squircle(0, 0, size); g.clip();
    g.drawImage(img, 0, 0, size, size);
  } else if (recipe === 'small') {
    // For 16 to 32 pixel uses (tray, browser tab): zoom in so the glowing
    // slot fills the icon, since six asterisks cannot survive that small.
    squircle(0, 0, size); g.clip();
    const s = size * 1.5, o = (size - s) / 2;
    g.drawImage(img, o, o, s, s);
  } else if (recipe === 'macos-small') {
    // The macOS shape and margin, with the zoomed artwork of 'small'.
    const s = size * 0.84, o = (size - s) / 2;
    squircle(o, o, s); g.clip();
    const z = s * 1.5;
    g.drawImage(img, (size - z) / 2, (size - z) / 2, z, z);
  } else if (recipe === 'macos') {
    // macOS icons sit inside a margin with a soft shadow.
    const s = size * 0.805, o = (size - s) / 2;
    g.save();
    g.shadowColor = 'rgba(0,0,0,0.35)'; g.shadowBlur = size * 0.02; g.shadowOffsetY = size * 0.01;
    squircle(o, o, s); g.fillStyle = '#0b1230'; g.fill();
    g.restore();
    squircle(o, o, s); g.clip();
    g.drawImage(img, o, o, s, s);
  } else if (recipe === 'android') {
    // Android launchers crop to the middle two thirds, so the artwork is
    // drawn smaller over a blurred copy of itself, which hides the seam.
    g.filter = `blur(${size * 0.06}px)`;
    g.drawImage(img, -size * 0.1, -size * 0.1, size * 1.2, size * 1.2);
    g.filter = 'none';
    const s = size * 0.8, o = (size - s) / 2;
    const layer = document.createElement('canvas');
    layer.width = layer.height = size;
    const l = layer.getContext('2d');
    l.imageSmoothingQuality = 'high';
    l.drawImage(img, o, o, s, s);
    const fade = l.createRadialGradient(size / 2, size / 2, s * 0.36, size / 2, size / 2, s * 0.5);
    fade.addColorStop(0, 'rgba(0,0,0,1)'); fade.addColorStop(1, 'rgba(0,0,0,0)');
    l.globalCompositeOperation = 'destination-in';
    l.fillStyle = fade; l.fillRect(0, 0, size, size);
    g.drawImage(layer, 0, 0);
  } // 'empty' stays transparent
  const blob = await new Promise((r) => c.toBlob(r, 'image/png'));
  return Array.from(new Uint8Array(await blob.arrayBuffer()));
}, { size, recipe });

const write = async (path, size, recipe) => {
  fs.writeFileSync(path, Buffer.from(await render(size, recipe)));
  console.log(path, size, recipe);
};
await write(`${dir}/icon.png`, 1024, 'square');
await write(`${dir}/macos.png`, 1024, 'macos');
await write(`${dir}/android-background.png`, 1024, 'android');
await write(`${dir}/android-foreground.png`, 1024, 'empty');
await write(`${dir}/rounded.png`, 512, 'rounded');
await write('../app/assets/tray.png', 64, 'small');
await write('../internal/web/static/icon.png', 192, 'small');

// The Windows tray needs an .ico; one can simply wrap a PNG.
const png = Buffer.from(await render(64, 'small'));
const head = Buffer.alloc(22);
head.writeUInt16LE(1, 2); head.writeUInt16LE(1, 4);
head[6] = 64; head[7] = 64; head.writeUInt16LE(1, 10); head.writeUInt16LE(32, 12);
head.writeUInt32LE(png.length, 14); head.writeUInt32LE(22, 18);
fs.writeFileSync('../app/assets/tray.ico', Buffer.concat([head, png]));

// Browser extension icons: the zoomed variant where it is drawn small.
fs.mkdirSync('../extension/icons', { recursive: true });
await write('../extension/icons/16.png', 16, 'small');
await write('../extension/icons/32.png', 32, 'small');
await write('../extension/icons/48.png', 48, 'rounded');
await write('../extension/icons/128.png', 128, 'rounded');

// Platform icon sets, then replace the smallest macOS sizes, where the
// full artwork is unreadable, with the zoomed variant.
execFileSync('dart', ['run', 'flutter_launcher_icons'], { cwd: '../app', stdio: 'ignore' });
const mac = '../app/macos/Runner/Assets.xcassets/AppIcon.appiconset';
await write(`${mac}/app_icon_16.png`, 16, 'macos-small');
await write(`${mac}/app_icon_32.png`, 32, 'macos-small');
await write(`${mac}/app_icon_64.png`, 64, 'macos-small');
await browser.close();
