// The toolbar popup: connect to a server, see recent codes, copy them,
// and make an address for the site in the current tab.
const api = globalThis.browser ?? globalThis.chrome;
const main = document.getElementById('main');

const clean = (kids) => kids.flat(Infinity).filter((k) => k != null && k !== false);

const el = (tag, attrs = {}, ...kids) => {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') n.className = v;
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
    else if (v !== false && v != null) n.setAttribute(k, v);
  }
  n.append(...clean(kids));
  return n;
};

// Replaces the popup's content. Like el, it skips absent children and
// flattens lists, which replaceChildren alone would print as text.
const put = (...kids) => main.replaceChildren(...clean(kids));

async function ask(message) {
  const res = await api.runtime.sendMessage(message);
  if (res?.error) throw Object.assign(new Error(res.error), { disconnected: res.disconnected });
  return res;
}

const showCode = (c) => /^\d{6}$/.test(c) ? `${c.slice(0, 3)} ${c.slice(3)}` : /^\d{8}$/.test(c) ? `${c.slice(0, 4)} ${c.slice(4)}` : c;
const ago = (ts) => {
  const s = Math.max(0, Math.floor(Date.now() / 1000 - ts));
  return s < 60 ? `${s} s ago` : s < 3600 ? `${Math.floor(s / 60)} min ago` : s < 86400 ? `${Math.floor(s / 3600)} h ago` : `${Math.floor(s / 86400)} d ago`;
};

async function currentSite() {
  try {
    const [tab] = await api.tabs.query({ active: true, currentWindow: true });
    const u = new URL(tab.url);
    return u.protocol.startsWith('http') ? u.hostname : '';
  } catch {
    return '';
  }
}

function connectView(error) {
  const server = el('input', { type: 'text', placeholder: 'mail.example.com', autocapitalize: 'none', spellcheck: 'false' });
  const status = el('p', { class: error ? 'error' : 'muted' }, error || 'Your browser opens your server so you can sign in with your passkey and approve this extension.');
  const form = el('form', { onsubmit: async (e) => {
    e.preventDefault();
    let origin;
    try {
      origin = new URL(server.value.includes('://') ? server.value : 'https://' + server.value).origin;
    } catch {
      status.className = 'error';
      status.textContent = "Enter your server's address, such as mail.example.com.";
      return;
    }
    // The extension may only talk to the one server the user names here.
    // A permission pattern names a host without its port.
    const { protocol, hostname } = new URL(origin);
    let granted = false;
    try {
      granted = await api.permissions.request({ origins: [`${protocol}//${hostname}/*`] });
    } catch (err) {
      console.warn('permission request failed', err);
    }
    if (!granted) {
      status.className = 'error';
      status.textContent = 'Codemail needs permission to reach your server.';
      return;
    }
    status.className = 'muted';
    status.textContent = 'Opening your server so you can sign in and approve this extension…';
    try {
      // This opens a tab, which closes the popup; the connection is
      // finished in the background and shown next time the popup opens.
      await ask({ type: 'connect', server: origin });
    } catch (err) {
      status.className = 'error';
      status.textContent = err.message;
    }
  } }, el('label', {}, 'Server address', server), el('button', { class: 'primary', type: 'submit', style: 'margin-top:10px;width:100%' }, 'Connect'));
  put(el('p', {}, 'Connect to your Codemail server.'), form, status);
  server.focus();
}

function messageItem(m, site, refresh) {
  const sub = [m.rcpt, ago(m.received_at), m.auth === 'pass' ? null : 'sender not verified'].filter(Boolean).join(' · ');
  const action = m.code
    ? el('button', { type: 'button', onclick: async (e) => {
      await navigator.clipboard.writeText(m.code);
      e.target.textContent = 'Copied';
      ask({ type: 'used', id: m.id }).catch(() => {});
    } }, 'Copy')
    : el('button', { type: 'button', onclick: () => {
      if (!m.link_matches_sender && !confirm(`This link goes to ${m.link_host}, which is not the sender's domain. Open it anyway?`)) return;
      api.tabs.create({ url: m.link });
      ask({ type: 'used', id: m.id }).catch(() => {});
    } }, 'Open link');
  const item = el('div', { class: 'item' },
    el('div', { class: 'grow' }, el('div', { class: 'title' }, m.service || m.from || 'Unknown sender'),
      m.code ? el('div', { class: 'code' }, showCode(m.code)) : el('div', { class: 'sub' }, `Link to ${m.link_host}`),
      el('div', { class: 'sub' }, sub)),
    action);
  // A verified sender whose domain differs from the site (a bank that
  // sends from another domain, say) can be linked once by the user.
  if (site && m.code && m.match === '' && m.auth === 'pass') {
    return [item, el('button', { class: 'plain', type: 'button', onclick: async () => {
      await ask({ type: 'trust', id: m.id, domain: site });
      refresh();
    } }, `Offer codes from ${m.sender_domain} on ${site} in future`)];
  }
  return item;
}

async function render() {
  let status;
  try {
    status = await ask({ type: 'status' });
  } catch (err) {
    return connectView(err.message);
  }
  document.getElementById('who').textContent = status.connected ? status.username : '';
  if (!status.connected) {
    return connectView(status.error || (status.waiting ? 'Finish signing in on the tab that opened, then open this again.' : ''));
  }

  const site = await currentSite();
  try {
    const [forSite, recent] = await Promise.all([ask({ type: 'codes', domain: site }), ask({ type: 'recent' })]);
    const fresh = forSite.messages || [];
    const freshIds = new Set(fresh.map((m) => m.id));
    const older = (recent.messages || []).filter((m) => !freshIds.has(m.id)).slice(0, 5);
    const note = el('p', { class: 'muted' });
    put(
      el('h2', {}, 'Just arrived'),
      fresh.length ? fresh.map((m) => messageItem(m, site, render)) : el('p', { class: 'muted' }, 'No codes in the last 15 minutes.'),
      older.length ? el('h2', {}, 'Earlier') : null,
      older.map((m) => messageItem(m, '', render)),
      site ? el('h2', {}, 'Signing up on this site?') : null,
      site ? el('button', { type: 'button', onclick: async (e) => {
        try {
          const res = await ask({ type: 'alias', label: site });
          await navigator.clipboard.writeText(res.alias.address);
          note.className = 'muted';
          note.textContent = `${res.alias.address} copied. Paste it into the email field.`;
        } catch (err) {
          note.className = 'error';
          note.textContent = err.message;
        }
      } }, `New address for ${site}`) : null,
      note,
      el('div', { class: 'row' },
        el('button', { type: 'button', onclick: () => api.tabs.create({ url: status.server }) }, 'Open Codemail'),
        el('button', { type: 'button', onclick: async () => { await ask({ type: 'disconnect' }); render(); } }, 'Disconnect')));
  } catch (err) {
    if (err.disconnected) return connectView('This browser was disconnected. Connect it again.');
    put(el('p', { class: 'error' }, err.message), el('button', { type: 'button', onclick: render }, 'Retry'));
  }
}

render();
