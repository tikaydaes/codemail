// Background worker: holds the connection to the user's Codemail server
// and answers requests from the popup and from pages' content scripts.
// It is the only part of the extension that sees tokens.

const api = globalThis.browser ?? globalThis.chrome;
const store = api.storage.local;

class Disconnected extends Error {
  constructor() { super('Not connected to a Codemail server.'); }
}

// --- helpers ---

const b64u = (buf) => btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
const random = (n) => b64u(crypto.getRandomValues(new Uint8Array(n)));

function normalizeServer(input) {
  let s = (input || '').trim();
  if (!s || /\s/.test(s)) return null;
  if (!s.includes('://')) s = 'https://' + s;
  try {
    const u = new URL(s);
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.origin : null;
  } catch {
    return null;
  }
}

async function saveTokens(server, t, extra = {}) {
  await store.set({ server, access: t.access_token, refresh: t.refresh_token, expires: Date.now() + (t.expires_in || 3600) * 1000, ...extra });
}

async function postToken(server, form) {
  const res = await fetch(server + '/oauth/token', { method: 'POST', body: new URLSearchParams(form) });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(body.error_description || `The server refused the sign-in (${body.error || res.status}).`);
    err.status = res.status;
    throw err;
  }
  return body;
}

// --- connecting ---

async function connect(serverInput) {
  const server = normalizeServer(serverInput);
  if (!server) throw new Error("Enter your server's address, such as mail.example.com.");
  let probe;
  try {
    probe = await fetch(server + '/api/v1/server');
  } catch {
    throw new Error(`Could not reach ${server}.`);
  }
  if (!probe.ok || !('registration' in (await probe.json().catch(() => ({}))))) {
    throw new Error(`${server} does not look like a Codemail server.`);
  }

  // OAuth with PKCE, in an ordinary tab: the user signs in on their own
  // server and approves this extension, and the server sends the tab to
  // its /oauth/return page with a one-time code. finishConnect picks that
  // up. What is needed to finish is saved first, because the browser may
  // stop this worker while the user is signing in.
  const verifier = random(48);
  const challenge = b64u(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier)));
  const state = random(16);
  const redirect = server + '/oauth/return';
  const ua = navigator.userAgent;
  const browserName = ua.includes('Firefox') ? 'Firefox' : ua.includes('Edg/') ? 'Edge' : ua.includes('Chrome') ? 'Chrome' : ua.includes('Safari') ? 'Safari' : 'Browser';
  await store.set({ pending: { server, verifier, state, redirect, at: Date.now() } });
  await api.tabs.create({
    url: server + '/oauth/authorize?' + new URLSearchParams({
      response_type: 'code', client_id: 'codemail-extension', redirect_uri: redirect,
      code_challenge: challenge, code_challenge_method: 'S256', state,
      device_name: `${browserName} extension`, platform: 'browser',
    }),
  });
}

// Completes a connection when a tab reaches the server's return page.
async function finishConnect(tabId, url) {
  const { pending } = await store.get('pending');
  if (!pending || !url.startsWith(pending.redirect + '?')) return;
  const params = new URL(url).searchParams;
  await store.remove('pending');
  try {
    if (Date.now() - pending.at > 10 * 60 * 1000) return await fail('That sign-in took too long. Connect again.');
    if (params.get('error')) return await fail('Access was denied.');
    const code = params.get('code');
    if (!code || params.get('state') !== pending.state) return await fail('The sign-in returned an unexpected reply.');
    const tokens = await postToken(pending.server, { grant_type: 'authorization_code', code, code_verifier: pending.verifier, redirect_uri: pending.redirect });
    await saveTokens(pending.server, tokens, { connectError: '' });
    const me = await call('GET', '/api/v1/me');
    await store.set({ username: me.user.username });
    updateBadge();
  } catch (err) {
    await fail(err.message);
  } finally {
    // The tab closes only once the outcome is stored, so a popup opened
    // the moment it disappears already shows the result.
    api.tabs.remove(tabId).catch(() => {});
  }
}

// The popup shows this the next time it opens.
const fail = (message) => store.set({ connectError: message });

// The browser reports one page load as several updates. They are handled
// one at a time, so that only the first finds the pending connection and
// exchanges the one-time code; a second exchange would fail and leave an
// error over a connection that worked.
let finishing = Promise.resolve();
api.tabs.onUpdated.addListener((tabId, change, tab) => {
  const url = change.url || (change.status === 'complete' ? tab.url : '');
  if (url) finishing = finishing.then(() => finishConnect(tabId, url)).catch(() => {});
});

async function disconnect() {
  const s = await store.get(['server', 'refresh']);
  await store.clear();
  if (s.server && s.refresh) {
    await fetch(s.server + '/oauth/revoke', { method: 'POST', body: new URLSearchParams({ token: s.refresh }) }).catch(() => {});
  }
}

// --- authenticated calls ---

let refreshing = null;

// Refresh tokens rotate, so concurrent callers must share one renewal.
function refresh() {
  refreshing ??= (async () => {
    try {
      const s = await store.get(['server', 'refresh']);
      if (!s.refresh) throw new Disconnected();
      try {
        const t = await postToken(s.server, { grant_type: 'refresh_token', refresh_token: s.refresh });
        await saveTokens(s.server, t);
        return t.access_token;
      } catch (err) {
        if (err.status === 400) {
          await store.clear(); // revoked from the web app
          throw new Disconnected();
        }
        throw err;
      }
    } finally {
      refreshing = null;
    }
  })();
  return refreshing;
}

async function call(method, path, body) {
  const s = await store.get(['server', 'access', 'expires']);
  if (!s.server || !s.access) throw new Disconnected();
  const send = (access) => fetch(s.server + path, {
    method,
    headers: { Authorization: 'Bearer ' + access, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  let res = await send(s.expires - Date.now() < 60000 ? await refresh() : s.access);
  if (res.status === 401) res = await send(await refresh());
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `Request failed (${res.status}).`);
  return data;
}

// --- toolbar badge ---

// The badge shows how many unused codes arrived in the last few minutes.
// The worker cannot hold a live connection (browsers stop idle workers),
// so it checks once a minute and whenever something changes.
async function updateBadge() {
  let count = 0;
  try {
    const res = await call('GET', '/api/v1/codes/for-site?domain=');
    count = (res.messages || []).length;
  } catch {
    // Not connected or offline: no badge.
  }
  await api.action.setBadgeBackgroundColor({ color: '#1F4FD8' });
  await api.action.setBadgeText({ text: count ? String(count) : '' });
}

api.alarms.create('badge', { periodInMinutes: 1 });
api.alarms.onAlarm.addListener((alarm) => { if (alarm.name === 'badge') updateBadge(); });
api.runtime.onInstalled.addListener(updateBadge);
api.runtime.onStartup.addListener(updateBadge);

// --- requests from the popup and content scripts ---

const handlers = {
  async status() {
    const s = await store.get(['server', 'access', 'username', 'pending', 'connectError']);
    if (s.connectError) await store.remove('connectError');
    if (s.access && !s.username) {
      // The popup can open just before the name is saved.
      try {
        s.username = (await call('GET', '/api/v1/me')).user.username;
        await store.set({ username: s.username });
      } catch { /* shown without a name */ }
    }
    return { connected: !!s.access, server: s.server || '', username: s.username || '', waiting: !!s.pending, error: s.connectError || '' };
  },
  connect: (m) => connect(m.server).then(() => ({})),
  disconnect: () => disconnect().then(updateBadge).then(() => ({})),
  // Codes received in the last few minutes, each marked with whether it
  // may be offered on the given site.
  async codes(m) {
    const res = await call('GET', '/api/v1/codes/for-site?domain=' + encodeURIComponent(m.domain || ''));
    api.action.setBadgeText({ text: res.messages?.length ? String(res.messages.length) : '' });
    return res;
  },
  recent: () => call('GET', '/api/v1/messages?kind=code&limit=10'),
  used: (m) => call('PATCH', `/api/v1/messages/${m.id}`, { used: true }).finally(updateBadge),
  // Opens a confirmation link a page asked for. The page supplies only a
  // message ID; the link and whether this site may use it come from the
  // server, so a page cannot make the extension open anything else.
  async open(m) {
    const res = await call('GET', '/api/v1/codes/for-site?domain=' + encodeURIComponent(m.domain || ''));
    const message = (res.messages || []).find((x) => x.id === m.id && x.match && x.link);
    if (!message) throw new Error('That link is not available on this site.');
    await api.tabs.create({ url: message.link });
    await call('PATCH', `/api/v1/messages/${message.id}`, { used: true });
    updateBadge();
    return {};
  },
  trust: (m) => call('POST', '/api/v1/site-links', { message_id: m.id, domain: m.domain }),
  alias: (m) => call('POST', '/api/v1/aliases/generate', { label: m.label }),
};

api.runtime.onMessage.addListener((message, sender, sendResponse) => {
  const handler = handlers[message?.type];
  if (!handler) return false;
  // A message comes either from the extension's own pages (the popup) or
  // from the content script in a web page. Web pages get the minimum.
  const fromWebPage = !(sender.url || '').startsWith(api.runtime.getURL(''));
  if (fromWebPage) {
    if (!['codes', 'used', 'open', 'status'].includes(message.type)) {
      sendResponse({ error: 'Not allowed from a page.' });
      return false;
    }
    // A page only ever learns about codes for its own site: the domain
    // is taken from the browser, not from what the content script says.
    if (message.type === 'codes' || message.type === 'open') {
      let domain = '';
      try { domain = new URL(sender.url).hostname; } catch { /* no usable URL: offer nothing */ }
      message = { ...message, domain };
    }
  }
  handler(message).then(
    (result) => sendResponse(result),
    (err) => sendResponse({ error: err.message, disconnected: err instanceof Disconnected }),
  );
  return true; // the reply is sent asynchronously
});
