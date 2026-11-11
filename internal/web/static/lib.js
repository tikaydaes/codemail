// Small helpers shared by the views: DOM building, API calls, formatting
// and the WebAuthn encoding that browsers do not do for us.

// h builds an element. Children are added as text nodes or elements, so
// values from the server are never parsed as HTML.
export function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k in el && k !== 'list' && k !== 'form') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid != null && kid !== false) el.append(kid);
  }
  return el;
}

// fill replaces an element's children, skipping absent ones the same way
// h does. (replaceChildren alone would print "false" or "null".)
export function fill(el, ...kids) {
  el.replaceChildren(...kids.flat(Infinity).filter((kid) => kid != null && kid !== false));
}

export async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  let data = {};
  try { data = await res.json(); } catch { /* empty or non-JSON reply */ }
  if (!res.ok) {
    const err = new Error(data.error || `Request failed (${res.status})`);
    err.status = res.status;
    throw err;
  }
  return data;
}

export function toast(message, isError) {
  const el = h('div', { class: 'toast' + (isError ? ' error' : ''), role: isError ? 'alert' : 'status' }, message);
  document.getElementById('toasts').append(el);
  setTimeout(() => el.remove(), isError ? 6000 : 3000);
}

// act runs an async action and reports its failure instead of losing it.
export async function act(fn) {
  try {
    return await fn();
  } catch (err) {
    if (err.name !== 'NotAllowedError' && err.name !== 'AbortError') toast(err.message, true);
    else toast('Cancelled', true);
  }
}

export async function copy(text, what) {
  await navigator.clipboard.writeText(text);
  toast(`${what || 'Copied'} to clipboard`);
}

// Codes are grouped for reading and copied without the spaces.
export function showCode(code) {
  if (/^\d{6}$/.test(code)) return code.slice(0, 3) + ' ' + code.slice(3);
  if (/^\d{8}$/.test(code)) return code.slice(0, 4) + ' ' + code.slice(4);
  return code;
}

export function ago(ts) {
  const s = Math.max(0, Math.floor(Date.now() / 1000 - ts));
  if (s < 60) return `${s} s ago`;
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return day(ts);
}

export function day(ts) {
  if (!ts) return 'Never';
  return new Date(ts * 1000).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
}

export function dateTime(ts) {
  if (!ts) return 'Never';
  return new Date(ts * 1000).toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export function field(label, input, hint) {
  return h('label', { class: 'field' }, label, input, hint && h('span', {}, hint));
}

// --- WebAuthn ---

const toBuf = (s) => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
const toB64 = (buf) => btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

export function passkeysSupported() {
  return !!(window.PublicKeyCredential && navigator.credentials);
}

export async function createPasskey(publicKey) {
  const opts = structuredClone(publicKey);
  opts.challenge = toBuf(opts.challenge);
  opts.user.id = toBuf(opts.user.id);
  for (const c of opts.excludeCredentials || []) c.id = toBuf(c.id);
  const cred = await navigator.credentials.create({ publicKey: opts });
  return {
    id: cred.id, rawId: toB64(cred.rawId), type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment || undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: toB64(cred.response.clientDataJSON),
      attestationObject: toB64(cred.response.attestationObject),
      transports: cred.response.getTransports ? cred.response.getTransports() : [],
    },
  };
}

export async function getPasskey(publicKey) {
  const opts = structuredClone(publicKey);
  opts.challenge = toBuf(opts.challenge);
  for (const c of opts.allowCredentials || []) c.id = toBuf(c.id);
  const cred = await navigator.credentials.get({ publicKey: opts });
  return {
    id: cred.id, rawId: toB64(cred.rawId), type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment || undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: toB64(cred.response.clientDataJSON),
      authenticatorData: toB64(cred.response.authenticatorData),
      signature: toB64(cred.response.signature),
      userHandle: cred.response.userHandle ? toB64(cred.response.userHandle) : undefined,
    },
  };
}
