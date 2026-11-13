// Account and server management screens.
import { h, fill, api, toast, act, copy, ago, day, field, createPasskey } from '/lib.js';
import { state, render, signOut, recoveryCodesView } from '/app.js';

const reload = () => render();

function statusPill(domain) {
  if (domain.verified && domain.failing_since) return h('span', { class: 'pill warn' }, 'DNS failing');
  return domain.verified ? h('span', { class: 'pill ok' }, 'Verified') : h('span', { class: 'pill warn' }, 'Waiting for DNS');
}

function recordsTable(domain) {
  const found = (v) => v === true ? h('span', { style: 'color:var(--ok);font-weight:600' }, 'Found')
    : v === false ? h('span', { style: 'color:var(--warn);font-weight:600' }, 'Not found') : h('span', { class: 'muted' }, 'Not checked');
  const table = (heads, rows) => h('div', { class: 'tablebox' }, h('table', {},
    h('thead', {}, h('tr', {}, heads.map((t) => h('th', {}, t)))), h('tbody', {}, rows)));
  const cells = (r) => [h('td', { class: 'mono' }, r.type), h('td', { class: 'mono wrap' }, r.name), h('td', { class: 'mono wrap' }, r.value)];
  return h('div', { class: 'stack', style: 'gap:10px' },
    table(['Type', 'Name', 'Value', 'Status'], domain.records.map((r) => h('tr', {}, cells(r), h('td', {}, found(r.found))))),
    (domain.recommended || []).length ? h('details', {},
      h('summary', {}, 'Recommended: stop others sending mail as this domain'),
      h('p', { class: 'muted small' }, 'Codemail only receives. If nothing else sends mail from this domain, add these records too. They tell other mail servers to reject messages that claim to come from it, so nobody can forge your addresses. Skip them, or keep the records you already have, if you send mail from this domain yourself.'),
      table(['Type', 'Name', 'Value'], domain.recommended.map((r) => h('tr', {}, cells(r))))) : null);
}

// --- addresses ---

export async function addressesView() {
  const data = await api('GET', '/api/v1/addresses');
  const me = data.username;
  const claimable = [...data.shared_domains.filter((d) => d.allow_aliases), ...data.domains.filter((d) => d.verified)];

  const builtIn = data.shared_domains.map((d) => h('section', { class: 'card', 'aria-label': `Addresses on ${d.name}` },
    h('h2', {}, `Always yours on ${d.name}`),
    h('div', { class: 'grid' },
      d.allow_mailbox && h('div', { class: 'tile' }, h('div', { class: 'mono small wrap' }, `${me}@${d.name}`), h('div', { class: 'muted small' }, 'Your mailbox')),
      d.allow_mailbox && h('div', { class: 'tile' }, h('div', { class: 'mono small wrap' }, `${me}+anything@${d.name}`), h('div', { class: 'muted small' }, 'Add a tag per service')),
      d.allow_subdomains && h('div', { class: 'tile' }, h('div', { class: 'mono small wrap' }, `anything@${me}.${d.name}`), h('div', { class: 'muted small' }, 'For sites that reject "+"')))));

  const domainSelect = () => h('select', {}, claimable.map((d) => h('option', { value: d.id }, '@' + d.name)));

  const genLabel = h('input', { type: 'text', placeholder: 'Northwind Bank', required: true });
  const genDomain = domainSelect();
  const generate = h('form', { class: 'row end', onsubmit: (e) => {
    e.preventDefault();
    act(async () => {
      const res = await api('POST', '/api/v1/aliases/generate', { label: genLabel.value, domain_id: Number(genDomain.value) });
      await copy(res.alias.address, 'New address copied');
      reload();
    });
  } }, field('Service name', genLabel), field('Domain', genDomain), h('button', { class: 'primary', type: 'submit' }, 'Generate address'));

  const local = h('input', { type: 'text', placeholder: 'travel', required: true, autocapitalize: 'none', spellcheck: false });
  const claimDomain = domainSelect();
  const label = h('input', { type: 'text', placeholder: 'Airlines and hotels' });
  const claim = h('form', { class: 'row end', onsubmit: (e) => {
    e.preventDefault();
    act(async () => {
      await api('POST', '/api/v1/aliases', { local: local.value, domain_id: Number(claimDomain.value), label: label.value });
      reload();
    });
  } }, field('Name', local), field('Domain', claimDomain), field('Label (optional)', label), h('button', { type: 'submit' }, 'Claim address'));

  const aliases = h('section', { class: 'card', 'aria-label': 'Your addresses' },
    h('h2', {}, 'Your addresses'),
    data.aliases.length ? h('div', { class: 'tablebox' }, h('table', {},
      h('thead', {}, h('tr', {}, h('th', {}, 'Address'), h('th', {}, 'For'), h('th', {}, 'Created'), h('th', {}, h('span', { hidden: true }, 'Actions')))),
      h('tbody', {}, data.aliases.map((a) => h('tr', {},
        h('td', { class: 'mono small wrap' }, a.address),
        h('td', {}, a.label || h('span', { class: 'muted' }, 'No label'),
          a.generated ? h('div', { class: 'muted small' }, a.expected_sender ? `Generated · expects ${a.expected_sender}` : 'Generated · no mail yet') : null),
        h('td', { class: 'muted' }, day(a.created_at)),
        h('td', { class: 'actions' },
          h('button', { type: 'button', onclick: () => act(() => copy(a.address, 'Address copied')) }, 'Copy'),
          h('button', { type: 'button', onclick: () => {
            const label = prompt(`What is ${a.address} for? This name is shown on mail that arrives there.`, a.label);
            if (label === null) return;
            act(async () => {
              await api('PATCH', `/api/v1/aliases/${a.id}`, { label });
              reload();
            });
          } }, 'Rename'),
          h('button', { class: 'danger', type: 'button', onclick: () => confirm(`Release ${a.address}? Mail to it will be rejected, and someone else could claim it.`) && act(async () => {
            await api('DELETE', `/api/v1/aliases/${a.id}`);
            reload();
          }) }, 'Release')))))))
      : h('p', { class: 'muted' }, 'You have not created any addresses yet.'),
    claimable.length ? [
      h('h3', {}, 'One address per service'),
      h('p', { class: 'muted small' }, 'Name the service and get an unguessable address just for it. If anyone else ever writes to that address, you will be warned.'),
      generate,
      h('h3', {}, 'Choose your own'),
      claim,
    ] : h('p', { class: 'muted' }, 'No domain on this server allows extra addresses yet.'));

  const newDomain = h('input', { type: 'text', placeholder: 'example.com', required: true, autocapitalize: 'none', spellcheck: false });
  const domains = h('section', { class: 'card', 'aria-label': 'Your domains' },
    h('h2', {}, 'Your domains'),
    h('p', { class: 'muted small' }, 'Point a domain you own at this server and every address on it comes to you.'),
    data.domains.map((d) => {
      const box = h('div', { class: 'card', style: 'gap:12px' });
      const draw = (dom) => fill(box, 
        h('div', { class: 'row between' }, h('div', { class: 'mono' }, dom.name), statusPill(dom)),
        !dom.verified && h('p', { class: 'muted small' }, 'Add these records at your DNS provider. Mail is accepted once both are found.'),
        dom.verified && dom.failing_since ? h('div', { class: 'notice warn' }, `The DNS records have been missing since ${day(dom.failing_since)}. Mail stops after 7 days.`) : null,
        recordsTable(dom),
        h('div', { class: 'row' },
          h('button', { class: dom.verified ? '' : 'primary', type: 'button', onclick: () => act(async () => {
            const res = await api('POST', `/api/v1/domains/${dom.id}/check`);
            draw(res.domain);
            toast(res.domain.verified ? `${dom.name} is verified` : 'Records not found yet. DNS changes can take a while.');
          }) }, 'Check DNS now'),
          h('button', { class: 'danger', type: 'button', onclick: () => confirm(`Remove ${dom.name}? Mail to it will be rejected.`) && act(async () => {
            await api('DELETE', `/api/v1/domains/${dom.id}`);
            reload();
          }) }, 'Remove')));
      draw(d);
      return box;
    }),
    h('form', { class: 'row end', onsubmit: (e) => {
      e.preventDefault();
      act(async () => {
        await api('POST', '/api/v1/domains', { name: newDomain.value });
        reload();
      });
    } }, field('Domain', newDomain), h('button', { type: 'submit' }, 'Add a domain')));

  return h('section', { class: 'page' }, h('h1', {}, 'Addresses'), builtIn, aliases, domains);
}

// --- devices ---

export async function devicesView() {
  const { devices } = await api('GET', '/api/v1/devices');
  const pairBox = h('div', { class: 'stack' });

  function startPairing() {
    act(async () => {
      const pair = await api('POST', '/api/v1/devices/pairing-code');
      const status = h('p', { class: 'muted', role: 'status' }, 'Waiting for a device to scan the code…');
      const confirmBox = h('div', {});
      fill(pairBox, 
        h('img', { class: 'qr', alt: 'Pairing QR code', src: `/api/v1/devices/pairing-code/${pair.id}/qr.png?code=${encodeURIComponent(pair.code)}` }),
        status, confirmBox,
        h('p', { class: 'muted small' }, 'Open the Codemail app on the device and choose Scan code. The code works once and expires in 2 minutes.'));
      let shown = false;
      clearInterval(state.timer);
      state.timer = setInterval(async () => {
        try {
          const st = await api('GET', `/api/v1/devices/pairing-code/${pair.id}`);
          if (st.claimed && !shown) {
            shown = true;
            status.textContent = '';
            fill(confirmBox, h('div', { class: 'notice ok stack' },
              h('div', {}, h('strong', {}, st.device_name), st.platform ? ` (${st.platform})` : '', ' wants to connect. Only confirm if this is your device.'),
              h('div', { class: 'row' }, h('button', { class: 'primary', type: 'button', onclick: () => act(async () => {
                await api('POST', `/api/v1/devices/pairing-code/${pair.id}/confirm`);
                toast(`${st.device_name} connected`);
                setTimeout(reload, 1500);
              }) }, 'Confirm device'), h('button', { type: 'button', onclick: reload }, 'Cancel'))));
          }
        } catch {
          clearInterval(state.timer);
          if (!shown) fill(pairBox, h('p', { class: 'muted' }, 'That code expired.'), h('button', { type: 'button', onclick: startPairing }, 'Show a new code'));
        }
      }, 2000);
    });
  }
  pairBox.append(h('button', { class: 'primary', type: 'button', style: 'align-self:flex-start', onclick: startPairing }, 'Add device'));

  return h('section', { class: 'page' }, h('h1', {}, 'Devices'),
    h('section', { class: 'card' }, h('h2', {}, 'Connected devices'),
      devices.length ? h('div', { class: 'tablebox' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', {}, 'Device'), h('th', {}, 'Connected'), h('th', {}, 'Last seen'), h('th', {}, h('span', { hidden: true }, 'Actions')))),
        h('tbody', {}, devices.map((d) => h('tr', {},
          h('td', {}, h('div', { class: 'title' }, d.name), h('div', { class: 'muted small' }, d.platform || 'Unknown platform')),
          h('td', { class: 'muted' }, day(d.created_at)), h('td', { class: 'muted' }, ago(d.last_seen)),
          h('td', { class: 'actions' }, h('button', { class: 'danger', type: 'button', onclick: () => confirm(`Disconnect ${d.name}? It will stop receiving codes.`) && act(async () => {
            await api('DELETE', `/api/v1/devices/${d.id}`);
            reload();
          }) }, 'Disconnect')))))))
        : h('p', { class: 'muted' }, 'No apps or browser extensions are connected.')),
    h('section', { class: 'card' }, h('h2', {}, 'Connect a phone with a QR code'),
      h('p', { class: 'muted small' }, 'Desktop apps and the browser extension connect by themselves: enter this server\'s address in the app and approve it here.'),
      pairBox));
}

// --- settings ---

export async function settingsView() {
  const [{ passkeys }, { sessions }] = await Promise.all([api('GET', '/api/v1/passkeys'), api('GET', '/api/v1/sessions')]);
  const limits = state.limits;
  const options = [[0, `Server default (${limits.default_retention_hours / 24} days)`], [1, '1 hour'], [24, '1 day'], [72, '3 days'], [168, '7 days'], [336, '14 days'], [720, '30 days']]
    .filter(([hrs]) => hrs <= limits.max_retention_hours);
  const retention = h('select', { onchange: () => act(async () => {
    state.me = (await api('PATCH', '/api/v1/me', { retention_hours: Number(retention.value) })).user;
    toast('Saved. Applies to mail received from now on.');
  }) }, options.map(([hrs, text]) => h('option', { value: hrs, selected: hrs === state.me.retention_hours }, text)));

  const keyName = h('input', { type: 'text', placeholder: 'Work laptop' });
  return h('section', { class: 'page' }, h('h1', {}, 'Settings'),
    h('section', { class: 'card' }, h('h2', {}, 'Passkeys'),
      passkeys.length < 2 && h('div', { class: 'notice warn' }, 'You have one passkey. Add a second on another device so losing one does not lock you out.'),
      h('div', { class: 'tablebox' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', {}, 'Name'), h('th', {}, 'Added'), h('th', {}, 'Last used'), h('th', {}, h('span', { hidden: true }, 'Actions')))),
        h('tbody', {}, passkeys.map((k) => h('tr', {},
          h('td', { class: 'title' }, k.name), h('td', { class: 'muted' }, day(k.created_at)), h('td', { class: 'muted' }, k.last_used ? ago(k.last_used) : 'Never'),
          h('td', { class: 'actions' }, h('button', { class: 'danger', type: 'button', disabled: passkeys.length < 2, onclick: () => confirm(`Remove the passkey "${k.name}"?`) && act(async () => {
            await api('DELETE', `/api/v1/passkeys/${k.id}`);
            reload();
          }) }, 'Remove'))))))),
      h('form', { class: 'row end', onsubmit: (e) => {
        e.preventDefault();
        act(async () => {
          const begin = await api('POST', '/api/v1/passkey/register/begin', {});
          const credential = await createPasskey(begin.publicKey);
          await api('POST', '/api/v1/passkey/register/finish', { session: begin.session, credential, name: keyName.value });
          toast('Passkey added');
          reload();
        });
      } }, field('Name for the new passkey', keyName), h('button', { type: 'submit' }, 'Add a passkey'))),

    h('section', { class: 'card' }, h('h2', {}, 'Signed-in browsers'),
      h('p', { class: 'muted small' }, 'Browsers signed in to your account with a passkey. Apps and extensions are listed under Devices.'),
      h('div', { class: 'tablebox' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', {}, 'Browser'), h('th', {}, 'Signed in'), h('th', {}, 'Last active'), h('th', {}, h('span', { hidden: true }, 'Actions')))),
        h('tbody', {}, sessions.map((se) => h('tr', {},
          h('td', { class: 'title' }, se.label || 'Unknown browser', se.current ? h('span', { class: 'pill ok', style: 'margin-left:8px' }, 'This browser') : null),
          h('td', { class: 'muted' }, se.created_at ? day(se.created_at) : 'Before this was recorded'),
          h('td', { class: 'muted' }, se.last_seen ? ago(se.last_seen) : 'Unknown'),
          h('td', { class: 'actions' }, se.current ? null : h('button', { class: 'danger', type: 'button', onclick: () => act(async () => {
            await api('DELETE', `/api/v1/sessions/${se.id}`);
            toast('That browser has been signed out');
            reload();
          }) }, 'Sign out'))))))),
      sessions.length > 1 && h('button', { type: 'button', style: 'align-self:flex-start', onclick: () => confirm('Sign out every other browser?') && act(async () => {
        for (const se of sessions) if (!se.current) await api('DELETE', `/api/v1/sessions/${se.id}`);
        reload();
      }) }, 'Sign out all other browsers')),

    h('section', { class: 'card' }, h('h2', {}, 'Recovery codes'),
      h('p', { class: 'muted small' }, 'Recovery codes sign you in if you lose every passkey. Making new ones cancels the old ones.'),
      h('button', { type: 'button', style: 'align-self:flex-start', onclick: () => confirm('Replace your recovery codes? The old ones stop working.') && act(async () => {
        const res = await api('POST', '/api/v1/recovery-codes');
        document.getElementById('app').replaceChildren(recoveryCodesView(res.recovery_codes, reload));
      }) }, 'Make new recovery codes')),

    h('section', { class: 'card' }, h('h2', {}, 'Keeping mail'),
      h('div', { class: 'row' }, field('Delete messages after', retention)),
      h('p', { class: 'muted small' }, `This server allows up to ${limits.max_retention_hours / 24} days.`)),

    h('section', { class: 'card' }, h('h2', {}, 'Account'),
      h('div', { class: 'row' },
        h('button', { type: 'button', onclick: signOut }, 'Sign out'),
        h('button', { class: 'danger', type: 'button', onclick: () => {
          if (prompt(`This deletes your account, addresses, domains and mail for good. Type your username (${state.me.username}) to confirm.`) !== state.me.username) return;
          act(async () => {
            await api('DELETE', '/api/v1/me');
            location.reload();
          });
        } }, 'Delete account'))));
}

// --- admin ---

export async function adminView() {
  const [status, settings, invites, users, domains, audit] = await Promise.all([
    api('GET', '/api/v1/admin/status'), api('GET', '/api/v1/admin/settings'), api('GET', '/api/v1/admin/invites'),
    api('GET', '/api/v1/admin/users'), api('GET', '/api/v1/admin/domains'), api('GET', '/api/v1/admin/audit?limit=20'),
  ]);

  // The audit log, in plain words.
  const actions = {
    register: 'created their account', recovery: 'signed in with a recovery code', delete_account: 'deleted their account',
    add_device: 'connected a device', revoke_session: 'signed out a browser', add_domain: 'added their domain', delete_domain: 'removed their domain',
    create_invite: 'created an invite code', delete_invite: 'revoked an invite code', update_user: 'changed the role of',
    delete_user: 'deleted the account', issue_recovery_code: 'issued a recovery code for',
    add_shared_domain: 'added the shared domain', update_shared_domain: 'changed the shared domain', delete_shared_domain: 'removed the shared domain',
    export_corrections: 'exported corrections', set_registration: 'set registration to', set_max_retention_hours: 'set the longest retention (hours) to',
    set_reject_auth_fail: 'set rejecting unverified mail to',
  };
  const auditBody = h('tbody', {});
  const auditMore = h('button', { type: 'button', style: 'align-self:flex-start' }, 'Show older');
  let oldest = 0;
  const addAudit = (entries) => {
    for (const e of entries) {
      oldest = e.id;
      auditBody.append(h('tr', {},
        h('td', { class: 'muted', style: 'white-space:nowrap' }, new Date(e.at * 1000).toLocaleString()),
        h('td', {}, h('strong', {}, e.actor || 'A deleted account'), ' ', actions[e.action] || e.action, ' ',
          ['register', 'recovery', 'delete_account', 'export_corrections'].includes(e.action) ? '' : h('span', { class: 'mono small wrap' }, e.target))));
    }
    auditMore.hidden = entries.length < 20;
  };
  addAudit(audit.entries);
  auditMore.addEventListener('click', () => act(async () => addAudit((await api('GET', `/api/v1/admin/audit?limit=20&before=${oldest}`)).entries)));
  const auditCard = h('section', { class: 'card' }, h('h2', {}, 'Activity'),
    h('p', { class: 'muted small' }, 'Account, device and administrative actions on this server. Mail content is never recorded here.'),
    h('div', { class: 'tablebox' }, h('table', {}, h('thead', {}, h('tr', {}, h('th', {}, 'When'), h('th', {}, 'What'))), auditBody)),
    auditMore);
  const tile = (label, value, note) => h('div', { class: 'card', style: 'gap:2px;padding:16px' },
    h('div', { class: 'muted small' }, label), h('div', { class: 'title' }, value), h('div', { class: 'muted small' }, note));
  const mb = status.bytes / 1048576;

  const regOption = (value, text) => h('label', { class: 'check' },
    h('input', { type: 'radio', name: 'reg', value, checked: settings.registration === value, onchange: () => act(async () => {
      await api('PUT', '/api/v1/admin/settings', { registration: value });
      toast('Saved');
      reload();
    }) }), text);

  const uses = h('input', { type: 'number', min: 0, value: 1 });
  const days = h('input', { type: 'number', min: 0, value: 30 });
  const maxDays = h('input', { type: 'number', min: 1, max: 365, value: settings.max_retention_hours / 24 });

  const registration = h('section', { class: 'card' }, h('h2', {}, 'Registration'),
    h('fieldset', {}, h('legend', {}, 'Who can create an account'),
      regOption('open', 'Anyone'), regOption('invite', 'People with an invite code'), regOption('closed', 'Nobody')),
    h('h3', {}, 'Invite codes'),
    invites.invites.length ? invites.invites.map((inv) => h('div', { class: 'row between', style: 'border-top:1px solid var(--line);padding-top:8px' },
      h('div', { class: 'grow' }, h('div', { class: 'mono wrap' }, inv.code),
        h('div', { class: 'muted small' }, `${inv.uses} of ${inv.max_uses || 'unlimited'} uses · `, inv.expires_at ? `expires ${day(inv.expires_at)}` : 'no expiry')),
      h('button', { type: 'button', onclick: () => act(() => copy(inv.code, 'Invite code copied')) }, 'Copy'),
      h('button', { class: 'danger', type: 'button', onclick: () => act(async () => {
        await api('DELETE', `/api/v1/admin/invites/${encodeURIComponent(inv.code)}`);
        reload();
      }) }, 'Revoke'))) : h('p', { class: 'muted small' }, 'No invite codes.'),
    h('form', { class: 'row end', onsubmit: (e) => {
      e.preventDefault();
      act(async () => {
        await api('POST', '/api/v1/admin/invites', { max_uses: Number(uses.value), expires_days: Number(days.value) });
        reload();
      });
    } }, field('Uses (0 = unlimited)', uses), field('Expires in days (0 = never)', days), h('button', { class: 'primary', type: 'submit' }, 'New invite code')));

  const policy = h('section', { class: 'card' }, h('h2', {}, 'Mail policy'),
    h('form', { class: 'row end', onsubmit: (e) => {
      e.preventDefault();
      act(async () => {
        await api('PUT', '/api/v1/admin/settings', { max_retention_hours: Number(maxDays.value) * 24 });
        toast('Saved');
      });
    } }, field('Longest a user may keep mail (days)', maxDays), h('button', { type: 'submit' }, 'Save')),
    h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: settings.reject_auth_fail, onchange: (e) => act(async () => {
      await api('PUT', '/api/v1/admin/settings', { reject_auth_fail: e.target.checked });
      toast('Saved');
    }) }), 'Reject mail that fails sender verification (DMARC)'),
    h('p', { class: 'muted small' }, 'When off, failing mail is accepted and shown with a warning.'));

  const domainCard = (d) => {
    const flag = (key, text) => h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: d[key], onchange: (e) => save({ [key]: e.target.checked }) }), text);
    const maxAliases = h('input', { type: 'number', min: 0, value: d.max_aliases });
    const maxGenerated = h('input', { type: 'number', min: 0, value: d.max_generated });
    const records = h('div', {}, recordsTable(d));
    const save = (patch) => act(async () => {
      const res = await api('PATCH', `/api/v1/admin/domains/${d.id}`, patch);
      Object.assign(d, res.domain);
      fill(records, recordsTable(d));
      toast('Saved');
    });
    return h('div', { class: 'card', style: 'gap:10px' },
      h('div', { class: 'row between' }, h('div', { class: 'mono' }, d.name),
        h('button', { class: 'danger', type: 'button', onclick: () => confirm(`Remove ${d.name}? Every address on it stops working.`) && act(async () => {
          await api('DELETE', `/api/v1/admin/domains/${d.id}`);
          reload();
        }) }, 'Remove')),
      h('fieldset', {}, h('legend', {}, 'Users may receive mail at'),
        flag('allow_mailbox', `username@${d.name}, with +tags`),
        flag('allow_aliases', 'Addresses they claim or generate'),
        flag('allow_subdomains', `anything@username.${d.name}`)),
      h('form', { class: 'row end', onsubmit: (e) => {
        e.preventDefault();
        save({ max_aliases: Number(maxAliases.value), max_generated: Number(maxGenerated.value) });
      } }, field('Claimed addresses per user (0 = unlimited)', maxAliases), field('Generated addresses per user (0 = unlimited)', maxGenerated),
        h('button', { type: 'submit' }, 'Save limits')),
      records,
      h('button', { type: 'button', style: 'align-self:flex-start', onclick: () => save({}) }, 'Check DNS now'));
  };
  const newShared = h('input', { type: 'text', placeholder: 'codes.example.com', required: true, autocapitalize: 'none', spellcheck: false });
  const shared = h('section', { class: 'card' }, h('h2', {}, 'Shared domains'),
    h('p', { class: 'muted small' }, 'Domains every user can have addresses on. They accept mail as soon as they are added.'),
    domains.domains.map(domainCard),
    h('form', { class: 'row end', onsubmit: (e) => {
      e.preventDefault();
      act(async () => {
        await api('POST', '/api/v1/admin/domains', { name: newShared.value });
        reload();
      });
    } }, field('Domain', newShared), h('button', { class: 'primary', type: 'submit' }, 'Add shared domain')));

  const usersCard = h('section', { class: 'card' }, h('h2', {}, 'Users'),
    h('div', { class: 'tablebox' }, h('table', {},
      h('thead', {}, h('tr', {}, ['User', 'Role', 'Passkeys', 'Own domains', 'Last sign-in'].map((t) => h('th', {}, t)), h('th', {}, h('span', { hidden: true }, 'Actions')))),
      h('tbody', {}, users.users.map((u) => h('tr', {},
        h('td', { class: 'title' }, u.username), h('td', {}, u.is_admin ? 'Admin' : 'User'), h('td', {}, u.passkeys), h('td', {}, u.domains),
        h('td', { class: 'muted' }, u.last_sign_in ? ago(u.last_sign_in) : day(u.created_at)),
        h('td', { class: 'actions' },
          h('button', { type: 'button', onclick: () => act(async () => {
            await api('PATCH', `/api/v1/admin/users/${u.id}`, { is_admin: !u.is_admin });
            reload();
          }) }, u.is_admin ? 'Remove admin' : 'Make admin'),
          h('button', { type: 'button', onclick: () => confirm(`Issue a one-time recovery code for ${u.username}? Anyone holding it can sign in as them.`) && act(async () => {
            const res = await api('POST', `/api/v1/admin/users/${u.id}/recovery-code`);
            prompt(`Give this recovery code to ${u.username}. It works once.`, res.recovery_code);
          }) }, 'Recovery code'),
          u.id !== state.me.id && h('button', { class: 'danger', type: 'button', onclick: () => confirm(`Delete ${u.username} and all their mail, addresses and domains?`) && act(async () => {
            await api('DELETE', `/api/v1/admin/users/${u.id}`);
            reload();
          }) }, 'Delete'))))))),
    h('p', { class: 'muted small' }, 'Admins manage accounts but have no way to read other users\' mail here.'));

  return h('section', { class: 'page' }, h('h1', {}, 'Admin'),
    h('div', { class: 'grid' },
      tile('Mail server', status.mail_host, status.last_message ? `Last message ${ago(status.last_message)}` : 'No mail received yet'),
      tile('Accounts', String(status.users), 'users'),
      tile('Storage', mb < 1 ? '< 1 MB' : `${mb.toFixed(0)} MB`, `${status.messages} messages kept`),
      tile('Corrections', String(status.corrections), status.corrections ? h('a', { href: '/api/v1/admin/corrections' }, 'Export for training') : 'None yet')),
    h('div', { class: 'columns' }, registration, policy),
    shared, usersCard, auditCard);
}
