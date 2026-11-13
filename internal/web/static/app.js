import { h, fill, api, toast, act, copy, showCode, ago, day, dateTime, field, passkeysSupported, createPasskey, getPasskey } from '/lib.js';
import { renderMarkdown } from '/md.js';
import { addressesView, devicesView, settingsView, adminView } from '/manage.js';

// Mail that came through one of the user's other mailboxes, by forwarding
// or because this server fetched it.
const viaMailbox = (m) => m.flags.includes('forwarded') || m.flags.includes('fetched');

const root = document.getElementById('app');
export const state = { me: null, server: null, limits: null, live: false, onEvent: null, timer: null };

const logo = () => h('img', { src: '/icon.png', alt: '', width: 28, height: 28, class: 'logo' });

// --- routing ---

const pages = [
  ['codes', 'Codes'], ['other', 'Other mail'], ['addresses', 'Addresses'],
  ['devices', 'Devices'], ['settings', 'Settings'], ['admin', 'Admin'],
];

function route() {
  if (location.pathname === '/oauth/authorize') return { name: 'oauth' };
  const [name, arg] = location.hash.replace(/^#\/?/, '').split('/');
  return { name: name || 'codes', arg };
}

export async function render() {
  clearInterval(state.timer);
  state.onEvent = null;
  const r = route();
  if (!state.me) {
    fill(root, authView(r.name === 'setup' ? r.arg : ''));
    return;
  }
  if (r.name === 'oauth') {
    fill(root, oauthView());
    return;
  }
  if (r.name === 'setup') {
    location.hash = '#/codes';
    return;
  }
  const main = h('main', { class: 'main' }, h('p', { class: 'loading' }, 'Loading…'));
  fill(root, h('div', { class: 'shell' }, navBar(r.name), main));
  const views = {
    codes: () => messagesView('code'), other: () => messagesView('other'),
    addresses: addressesView, devices: devicesView, settings: settingsView, admin: adminView,
  };
  try {
    const view = views[r.name] || views.codes;
    fill(main, ...[await view()].flat());
  } catch (err) {
    if (err.status === 401) return signedOut();
    fill(main, h('section', { class: 'page' }, h('div', { class: 'notice danger', role: 'alert' }, err.message)));
  }
}

function navBar(current) {
  return h('nav', { class: 'nav', 'aria-label': 'Main' },
    h('div', { class: 'brand' }, logo(), 'Codemail'),
    pages.filter(([name]) => name !== 'admin' || state.me.is_admin).map(([name, label]) =>
      h('a', { href: '#/' + name, 'aria-current': name === current ? 'page' : null }, label)),
    h('div', { class: 'who' }, 'Signed in as ', h('strong', {}, state.me.username)));
}

function signedOut() {
  state.me = null;
  if (state.events) state.events.close();
  state.events = null;
  render();
}

export async function signOut() {
  await act(() => api('POST', '/api/v1/logout'));
  signedOut();
}

// --- live events ---

function connectEvents() {
  if (state.events) return;
  const es = new EventSource('/api/v1/events');
  state.events = es;
  const setLive = (live) => {
    state.live = live;
    for (const el of document.querySelectorAll('[data-live]')) drawLive(el);
  };
  es.onopen = () => setLive(true);
  es.onerror = () => setLive(false); // the browser reconnects by itself
  for (const type of ['message', 'update', 'delete']) {
    es.addEventListener(type, (e) => {
      const data = JSON.parse(e.data);
      if (type === 'message' && data.kind === 'code') {
        toast(data.code ? `${data.service}: ${showCode(data.code)}` : `${data.service}: link received`);
      }
      if (state.onEvent) state.onEvent(type, data);
    });
  }
}

function drawLive(el) {
  el.className = 'pill ' + (state.live ? 'ok' : 'off');
  fill(el, h('span', { class: 'dot', 'aria-hidden': 'true' }), state.live ? 'Live' : 'Reconnecting');
}

function livePill() {
  const el = h('span', { 'data-live': '' });
  drawLive(el);
  return el;
}

// --- sign in, register, recover ---

async function afterSignIn() {
  const me = await api('GET', '/api/v1/me');
  state.me = me.user;
  state.limits = me.server;
  connectEvents();
  render();
}

function authView(setupToken) {
  const mode = state.server.needs_setup ? 'setup' : state.server.registration;
  const wrap = h('div', { class: 'auth' },
    h('div', { style: 'text-align:center' }, h('div', { class: 'brand', style: 'padding:0;font-size:28px' }, logo(), 'Codemail'),
      h('div', { class: 'muted' }, state.server.mail_host)));
  if (!passkeysSupported()) {
    wrap.append(h('div', { class: 'notice danger', role: 'alert' }, 'This browser does not support passkeys, which Codemail needs to sign you in.'));
    return wrap;
  }
  const cards = h('div', { class: 'cards' });
  wrap.append(cards);

  const signIn = h('section', { class: 'card', 'aria-label': 'Sign in' },
    h('h1', { style: 'font-size:22px' }, 'Sign in'),
    h('p', { class: 'muted' }, 'No username or password. Your device asks for your fingerprint, face or PIN.'),
    h('button', { class: 'primary', type: 'button', onclick: () => act(async () => {
      const begin = await api('POST', '/api/v1/passkey/login/begin', {});
      const credential = await getPasskey(begin.publicKey);
      await api('POST', '/api/v1/passkey/login/finish', { session: begin.session, credential });
      await afterSignIn();
    }) }, 'Sign in with a passkey'),
    recoveryForm());

  const username = h('input', { type: 'text', autocomplete: 'username', autocapitalize: 'none', spellcheck: false });
  const invite = h('input', { type: 'text', autocomplete: 'off', spellcheck: false, class: 'mono' });
  const register = h('form', { class: 'card', 'aria-label': 'Create account', onsubmit: (e) => {
    e.preventDefault();
    act(async () => {
      const begin = await api('POST', '/api/v1/passkey/register/begin',
        { username: username.value, invite: invite.value, setup: setupToken });
      const credential = await createPasskey(begin.publicKey);
      const done = await api('POST', '/api/v1/passkey/register/finish', { session: begin.session, credential });
      fill(root, recoveryCodesView(done.recovery_codes, afterSignIn));
    });
  } },
    h('h2', { style: 'font-size:22px' }, mode === 'setup' ? 'Set up this server' : 'Create an account'),
    mode === 'setup' && h('p', { class: 'muted' }, 'This first account becomes the administrator.'),
    field('Username', username, 'Lowercase letters, digits and hyphens. It becomes part of your addresses.'),
    mode === 'invite' && field('Invite code', invite, 'This server only accepts people with an invite.'),
    h('button', { class: 'primary', type: 'submit' }, 'Create passkey'));

  if (mode === 'setup') {
    cards.append(setupToken ? register
      : h('div', { class: 'notice warn' }, 'This server has no accounts yet. Open the setup link printed in its log to create the administrator account.'));
  } else {
    cards.append(signIn);
    if (mode !== 'closed') cards.append(register);
  }
  return wrap;
}

function recoveryForm() {
  const username = h('input', { type: 'text', autocomplete: 'username', autocapitalize: 'none' });
  const code = h('input', { type: 'text', autocomplete: 'off', class: 'mono', placeholder: 'XXXX-XXXX' });
  const form = h('form', { class: 'stack', hidden: true, onsubmit: (e) => {
    e.preventDefault();
    act(async () => {
      await api('POST', '/api/v1/recovery', { username: username.value, code: code.value });
      toast('Signed in. Add a new passkey in Settings.');
      location.hash = '#/settings';
      await afterSignIn();
    });
  } }, field('Username', username), field('Recovery code', code), h('button', { type: 'submit' }, 'Sign in with recovery code'));
  return h('div', { class: 'stack' },
    h('button', { class: 'plain', type: 'button', style: 'align-self:flex-start', onclick: () => { form.hidden = !form.hidden; } },
      'Lost your passkey? Use a recovery code'),
    form);
}

export function recoveryCodesView(codes, done) {
  const saved = h('input', { type: 'checkbox' });
  const go = h('button', { class: 'primary', type: 'button', disabled: true, onclick: done }, 'Continue');
  saved.addEventListener('change', () => { go.disabled = !saved.checked; });
  return h('div', { class: 'auth' }, h('section', { class: 'card', style: 'max-width:420px;padding:24px' },
    h('h1', { style: 'font-size:22px' }, 'Save your recovery codes'),
    h('p', { class: 'muted' }, 'Each code works once if you lose every passkey. They are shown only now.'),
    h('div', { class: 'codes-grid' }, codes.map((c) => h('span', {}, c))),
    h('div', { class: 'row' },
      h('button', { type: 'button', onclick: () => act(() => copy(codes.join('\n'), 'Recovery codes copied')) }, 'Copy'),
      h('a', { class: 'button', download: 'codemail-recovery-codes.txt',
        href: 'data:text/plain;charset=utf-8,' + encodeURIComponent(codes.join('\n') + '\n') }, 'Download')),
    h('label', { class: 'check' }, saved, 'I have saved these codes'),
    go));
}

// --- OAuth approval for apps and the extension ---

function oauthView() {
  const p = new URLSearchParams(location.search);
  const redirect = p.get('redirect_uri') || '';
  let target = redirect;
  try { target = new URL(redirect).protocol === 'codemail:' ? 'the codemail app' : new URL(redirect).host; } catch { /* shown as given */ }
  const deny = () => {
    try {
      const u = new URL(redirect);
      u.searchParams.set('error', 'access_denied');
      if (p.get('state')) u.searchParams.set('state', p.get('state'));
      location.href = u.toString();
    } catch { location.href = '/'; }
  };
  return h('div', { class: 'auth' }, h('section', { class: 'card', style: 'max-width:440px;padding:24px' },
    h('h1', { style: 'font-size:22px' }, 'Connect a device'),
    h('p', {}, h('strong', {}, p.get('device_name') || 'An app'), p.get('platform') ? ` (${p.get('platform')})` : '',
      ' wants to read your codes and messages as ', h('strong', {}, state.me.username), '.'),
    h('p', { class: 'muted small' }, 'You will be returned to ', h('span', { class: 'mono wrap' }, target),
      '. You can disconnect the device at any time under Devices.'),
    h('div', { class: 'row' },
      h('button', { class: 'primary', type: 'button', onclick: () => act(async () => {
        const res = await api('POST', '/api/v1/oauth/approve', {
          client_id: p.get('client_id') || '', redirect_uri: redirect, state: p.get('state') || '',
          code_challenge: p.get('code_challenge') || '', code_challenge_method: p.get('code_challenge_method') || '',
          device_name: p.get('device_name') || '', platform: p.get('platform') || '',
        });
        location.href = res.redirect;
      }) }, 'Allow'),
      h('button', { type: 'button', onclick: deny }, 'Deny'))));
}

// --- codes and other mail ---

const freshFor = 600; // seconds a code is shown as current

async function messagesView(kind) {
  let messages = (await api('GET', `/api/v1/messages?kind=${kind}&limit=100`)).messages;
  let selected = null;
  let detail = null;
  const search = h('input', { type: 'search', placeholder: 'Service, address or code' });
  const listBox = h('div', { class: 'stack' });
  const aside = h('aside', { class: 'aside', 'aria-label': 'Selected message', hidden: true });

  const isFresh = (m) => m.code && !m.used_at && Date.now() / 1000 - m.received_at < freshFor;

  async function useCode(m) {
    await copy(m.code, 'Code copied');
    const res = await api('PATCH', `/api/v1/messages/${m.id}`, { used: true });
    replace(res.message);
  }

  function openLink(m) {
    if (!m.link_matches_sender && !confirm(`This link goes to ${m.link_host}, which is not the sender's domain (${m.sender_domain || 'unknown'}). Open it anyway?`)) return;
    window.open(m.link, '_blank', 'noopener,noreferrer');
    act(async () => replace((await api('PATCH', `/api/v1/messages/${m.id}`, { used: true })).message));
  }

  function actionButton(m, primary) {
    if (m.code) return h('button', { type: 'button', class: primary ? 'primary' : '', onclick: () => act(() => useCode(m)) }, primary ? 'Copy code' : 'Copy');
    if (m.link) return h('button', { type: 'button', class: primary ? 'primary' : '', onclick: () => openLink(m) }, 'Open link');
    return null;
  }

  function replace(m) {
    const i = messages.findIndex((x) => x.id === m.id);
    const belongs = m.kind === kind;
    if (i >= 0 && belongs) messages[i] = m;
    else if (i >= 0) messages.splice(i, 1);
    else if (belongs) messages.unshift(m);
    messages.sort((a, b) => b.id - a.id);
    drawList();
    if (selected === m.id) belongs ? select(m.id) : close();
  }

  function row(m) {
    const stale = !!m.used_at || Date.now() / 1000 - m.received_at >= freshFor;
    return h('div', { class: 'item' + (selected === m.id ? ' selected' : '') + (stale ? ' stale' : '') },
      h('button', { class: 'open', type: 'button', onclick: () => select(m.id) },
        h('div', { class: 'title' }, m.service || m.from || '(unknown sender)'),
        h('div', { class: 'muted small wrap' }, m.subject || '(no subject)'),
        h('div', { class: 'muted small wrap' }, `${m.rcpt} · ${ago(m.received_at)}`, m.used_at ? ' · used' : '')),
      m.flags.includes('unexpected_sender') && h('span', { class: 'pill warn' }, 'Unexpected sender'),
      viaMailbox(m) && m.auth !== 'pass' && h('span', { class: 'pill warn' }, 'Sender not verified'),
      m.code ? h('div', { class: 'code-mid' }, showCode(m.code))
        : m.link ? h('div', { class: 'muted small' }, 'Link to ', m.link_host) : null,
      actionButton(m, false));
  }

  function drawList() {
    const q = search.value.trim();
    const shown = messages;
    const fresh = kind === 'code' && !q ? shown.find(isFresh) : null;
    const rest = shown.filter((m) => m !== fresh);
    fill(listBox, 
      fresh && h('article', { class: 'card fresh' },
        h('div', { class: 'row between' },
          h('div', {}, h('div', { class: 'title', style: 'font-size:16px' }, fresh.service), h('div', { class: 'muted small' }, fresh.subject)),
          h('span', { class: 'pill warn' }, ago(fresh.received_at))),
        h('div', { class: 'row' }, h('div', { class: 'code-big' }, showCode(fresh.code)), actionButton(fresh, true)),
        h('div', { class: 'row between' },
          h('div', { class: 'muted small wrap' }, `Sent to ${fresh.rcpt} · `, fresh.auth === 'pass' ? 'Sender verified' : fresh.auth === 'fail' ? 'Sender NOT verified' : 'Sender not checked'),
          h('button', { class: 'plain', type: 'button', onclick: () => select(fresh.id) }, 'Details'))),
      rest.length ? h('div', { class: 'section-label' }, fresh ? 'Earlier' : q ? 'Results' : kind === 'code' ? 'Recent' : 'Messages') : null,
      rest.length ? h('div', { class: 'list' }, rest.map(row))
        : !fresh && h('div', { class: 'list' }, h('div', { class: 'empty' },
          q ? 'Nothing matches that search.' : kind === 'code' ? ['No codes yet. Mail sent to your addresses appears here the moment it arrives. No address yet? ', h('a', { href: '#/addresses' }, 'Choose one under Addresses'), '.'] : 'Mail without a code or link appears here.')));
  }

  function close() {
    selected = null;
    aside.hidden = true;
    drawList();
  }

  async function select(id) {
    selected = id;
    drawList();
    aside.hidden = false;
    try {
      detail = (await api('GET', `/api/v1/messages/${id}`)).message;
    } catch (err) {
      fill(aside, h('div', { class: 'notice danger' }, err.message));
      return;
    }
    const m = detail;
    const authText = { pass: viaMailbox(m) ? 'Verified' : 'Verified (SPF or DKIM passed)', fail: 'Failed: this may not be from who it claims', none: viaMailbox(m) ? 'Could not be verified' : 'Not checked' }[m.auth];
    fill(aside, 
      h('div', { class: 'row between' },
        h('div', { class: 'grow' }, h('h2', { style: 'font-size:18px' }, m.subject || '(no subject)'), h('div', { class: 'muted small wrap' }, m.from)),
        h('button', { type: 'button', onclick: close, 'aria-label': 'Close message' }, 'Close')),
      m.flags.includes('unexpected_sender') && h('div', { class: 'notice warn', role: 'alert' },
        'This address was generated for another sender. It may have been leaked or sold.'),
      m.auth === 'fail' && h('div', { class: 'notice danger', role: 'alert' }, 'The sender could not be verified. Treat this message with suspicion.'),
      viaMailbox(m) && m.auth === 'none' && h('div', { class: 'notice warn', role: 'alert' },
        `This message ${m.flags.includes('fetched') ? 'was fetched from' : 'was forwarded from'} your other mailbox, and nothing on it proves who sent it. Check it is what you expected before using the code.`),
      h('dl', { class: 'facts' },
        h('dt', {}, 'To'), h('dd', {}, m.rcpt),
        h('dt', {}, 'Received'), h('dd', {}, dateTime(m.received_at)),
        h('dt', {}, 'Sender check'), h('dd', {}, authText, m.auth_detail ? ` (${m.auth_detail})` : ''),
        h('dt', {}, 'Found'), h('dd', {}, m.code ? `Code ${showCode(m.code)}` : m.link ? `Link to ${m.link_host}` : 'Nothing',
          m.confidence ? `, ${Math.round(m.confidence * 100)}% confidence` : ''),
        m.link && h('dt', {}, 'Link'), m.link && h('dd', {}, m.link_matches_sender ? "Goes to the sender's own domain" : "Does NOT go to the sender's domain"),
        h('dt', {}, 'Deleted on'), h('dd', {}, day(m.expires_at))),
      m.markdown ? h('div', { class: 'body' }, renderMarkdown(m.markdown)) : h('p', { class: 'muted' }, '(empty message)'),
      h('div', { class: 'row' },
        actionButton(m, true),
        m.link && h('button', { type: 'button', onclick: () => act(() => copy(m.link, 'Link copied')) }, 'Copy link'),
        h('a', { class: 'button', href: `/api/v1/messages/${id}/raw` }, 'Download original'),
        h('button', { type: 'button', onclick: () => correctionDialog(m, replace) }, m.code || m.link ? 'Wrong code?' : 'Missed a code?'),
        m.used_at ? h('button', { type: 'button', onclick: () => act(async () => replace((await api('PATCH', `/api/v1/messages/${id}`, { used: false })).message)) }, 'Mark unused') : null,
        h('button', { class: 'danger', type: 'button', onclick: () => act(async () => {
          await api('DELETE', `/api/v1/messages/${id}`);
          messages = messages.filter((x) => x.id !== id);
          close();
        }) }, 'Delete')));
  }

  // The list loads 100 messages at a time.
  const pageSize = 100;
  const query = () => `kind=${kind}&limit=${pageSize}&q=${encodeURIComponent(search.value.trim())}`;
  const more = h('button', { type: 'button', style: 'align-self:flex-start', hidden: messages.length < pageSize, onclick: () => act(async () => {
    const older = (await api('GET', `/api/v1/messages?${query()}&before=${messages[messages.length - 1].id}`)).messages;
    messages = messages.concat(older);
    more.hidden = older.length < pageSize;
    drawList();
  }) }, 'Show older messages');

  let searchTimer;
  search.addEventListener('input', () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => act(async () => {
      messages = (await api('GET', `/api/v1/messages?${query()}`)).messages;
      more.hidden = messages.length < pageSize;
      drawList();
    }), 250);
  });

  state.onEvent = (type, data) => {
    if (type === 'delete') {
      messages = messages.filter((m) => m.id !== data.id);
      selected === data.id ? close() : drawList();
    } else if (!search.value.trim()) {
      replace(data);
    }
  };
  state.timer = setInterval(drawList, 15000); // keeps "12 s ago" and staleness current

  drawList();
  return [
    h('section', { class: 'page', 'aria-label': kind === 'code' ? 'Codes' : 'Other mail' },
      h('div', { class: 'row' },
        h('h1', { class: 'grow' }, kind === 'code' ? 'Codes' : 'Other mail'), livePill(),
        field('Search', search)),
      listBox,
      more,
      h('p', { class: 'muted small' }, 'Codes older than 10 minutes are greyed out. Messages are deleted automatically; see Settings.')),
    aside,
  ];
}

// correctionDialog lets the user say what the code or link really is.
async function correctionDialog(m, done) {
  const { candidates } = await act(() => api('GET', `/api/v1/messages/${m.id}/candidates`)) || {};
  if (!candidates) return;
  const group = (kind, current, noneLabel) => {
    const name = 'pick-' + kind;
    const options = candidates.filter((c) => c.kind === kind);
    return h('fieldset', {}, h('legend', {}, kind === 'code' ? 'Which is the sign-in code?' : 'Which is the confirmation link?'),
      options.map((c) => h('label', { class: 'check' },
        h('input', { type: 'radio', name, value: c.value, checked: c.value === current }),
        h('span', { class: 'wrap' }, h('span', { class: 'mono' }, kind === 'code' ? showCode(c.value) : c.value.slice(0, 70)),
          c.context ? h('span', { class: 'muted small' }, ' — ', c.context.slice(0, 80)) : ''))),
      h('label', { class: 'check' }, h('input', { type: 'radio', name, value: '', checked: !current }), noneLabel));
  };
  const dialog = h('dialog', {});
  const form = h('form', { class: 'stack', method: 'dialog', onsubmit: (e) => {
    e.preventDefault();
    act(async () => {
      let latest = m;
      for (const kind of ['code', 'link']) {
        const value = new FormData(form).get('pick-' + kind);
        if (value !== (m[kind] || '')) {
          latest = (await api('POST', `/api/v1/messages/${m.id}/correction`, { kind, value })).message;
        }
      }
      dialog.close();
      toast('Thanks. The correction is saved on this server.');
      done(latest);
    });
  } },
    h('h2', {}, 'Correct this message'),
    group('code', m.code, 'There is no sign-in code'),
    group('link', m.link, 'There is no confirmation link'),
    h('p', { class: 'muted small' }, 'Corrections stay on this server and help improve extraction.'),
    h('div', { class: 'row' }, h('button', { class: 'primary', type: 'submit' }, 'Save'),
      h('button', { type: 'button', onclick: () => dialog.close() }, 'Cancel')));
  dialog.append(form);
  dialog.addEventListener('close', () => dialog.remove());
  document.body.append(dialog);
  dialog.showModal();
}

// --- start ---

window.addEventListener('hashchange', render);

(async () => {
  try {
    state.server = await api('GET', '/api/v1/server');
    try {
      const me = await api('GET', '/api/v1/me');
      state.me = me.user;
      state.limits = me.server;
      connectEvents();
    } catch (err) {
      if (err.status !== 401) throw err;
    }
    render();
  } catch (err) {
    fill(root, h('div', { class: 'auth' }, h('div', { class: 'notice danger', role: 'alert' }, 'Could not reach the server: ' + err.message)));
  }
})();
