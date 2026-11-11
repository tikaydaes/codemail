// Runs in every page. When the user focuses a field that asks for a
// sign-in code, it offers the matching code from their Codemail server.
//
// The page itself must never see a code before the user chooses to fill
// it, so the offer is drawn inside a closed shadow root that page scripts
// cannot read.
(() => {
  const api = globalThis.browser ?? globalThis.chrome;
  if (!api?.runtime?.id) return;

  const HINT = /otp|one[-_ ]?time|verif|2fa|mfa|passcode|security.?code|auth.?code|\bcode\b|\bpin\b/i;
  const NOT_A_CODE = /postal|post.?code|zip|promo|coupon|discount|voucher|gift|country|area.?code|referral/i;
  const POLL_MS = 2500;
  const GIVE_UP_MS = 3 * 60 * 1000;

  const labelText = (el) => [...(el.labels || [])].map((l) => l.textContent).join(' ');

  /** Single-character inputs that together make one code box. */
  function boxesOf(el) {
    const scope = el.closest('form') || el.parentElement?.parentElement || document;
    return [...scope.querySelectorAll('input')].filter((i) => i.maxLength === 1 && i.offsetParent !== null);
  }

  function isCodeField(el) {
    if (!(el instanceof HTMLInputElement) || el.disabled || el.readOnly) return false;
    if (el.autocomplete === 'one-time-code') return true;
    if (!['text', 'tel', 'number'].includes(el.type)) return false;
    const hint = [el.name, el.id, el.placeholder, el.getAttribute('aria-label'), labelText(el)].join(' ');
    if (NOT_A_CODE.test(hint)) return false;
    if (el.maxLength === 1) return boxesOf(el).length >= 4;
    return HINT.test(hint) && (el.maxLength < 0 || el.maxLength <= 12);
  }

  /** Sets a value the way typing does, so page frameworks notice. */
  function type(el, value) {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
  }

  function fill(field, code) {
    const boxes = field.maxLength === 1 ? boxesOf(field) : [];
    if (boxes.length >= code.length) {
      [...code].forEach((ch, i) => type(boxes[i], ch));
      boxes[Math.min(code.length, boxes.length) - 1].focus();
    } else {
      type(field, code);
    }
  }

  const showCode = (c) => /^\d{6}$/.test(c) ? `${c.slice(0, 3)} ${c.slice(3)}` : /^\d{8}$/.test(c) ? `${c.slice(0, 4)} ${c.slice(4)}` : c;

  // --- the offer shown under the field ---

  let host, root, field, timer, started;
  const dismissed = new WeakSet();

  function ensureWidget() {
    if (host) return;
    host = document.createElement('codemail-offer');
    host.style.cssText = 'all:initial;position:fixed;z-index:2147483647;display:none';
    root = host.attachShadow({ mode: 'closed' });
    root.innerHTML = `<style>
      :host { all: initial; }
      .box { font: 13px/1.4 system-ui, sans-serif; color: #eceef2; background: #111a3a; border: 1px solid #2b6f8f;
        border-radius: 10px; box-shadow: 0 8px 24px rgba(0,0,0,.35); min-width: 220px; max-width: 320px; overflow: hidden; }
      button { all: unset; box-sizing: border-box; cursor: pointer; }
      .fill { display: flex; width: 100%; align-items: center; justify-content: space-between; gap: 14px; padding: 10px 12px; }
      .fill:hover, .fill:focus-visible { background: #1b2a5c; }
      .code { font: 600 19px ui-monospace, Menlo, Consolas, monospace; letter-spacing: .08em; color: #9ff3ff; }
      .from { font-size: 12px; color: #a9b4d0; }
      .go { font-weight: 600; color: #9ff3ff; }
      .note { display: flex; justify-content: space-between; gap: 10px; padding: 8px 12px; color: #a9b4d0; }
      .close { padding: 0 4px; color: #a9b4d0; font-size: 15px; }
    </style><div class="box" role="dialog" aria-label="Codemail"></div>`;
    document.documentElement.append(host);
    addEventListener('scroll', place, true);
    addEventListener('resize', place);
  }

  function place() {
    if (!host || host.style.display === 'none' || !field?.isConnected) return;
    const r = field.getBoundingClientRect();
    host.style.left = Math.max(8, Math.min(r.left, innerWidth - 330)) + 'px';
    host.style.top = Math.min(r.bottom + 6, innerHeight - 70) + 'px';
  }

  function hide() {
    clearInterval(timer);
    timer = null;
    if (host) host.style.display = 'none';
  }

  function render(offer) {
    const box = root.querySelector('.box');
    box.textContent = '';
    const el = (tag, cls, text) => { const n = document.createElement(tag); n.className = cls; if (text) n.textContent = text; return n; };
    if (offer) {
      const btn = el('button', 'fill');
      btn.type = 'button';
      const left = el('span', '');
      left.append(el('div', 'code', showCode(offer.code)), el('div', 'from', `From ${offer.service || offer.sender_domain} · Codemail`));
      btn.append(left, el('span', 'go', 'Fill'));
      // mousedown, so the field does not lose focus before the click lands
      btn.addEventListener('mousedown', (e) => {
        e.preventDefault();
        const target = field;
        hide();
        dismissed.add(target);
        fill(target, offer.code);
        api.runtime.sendMessage({ type: 'used', id: offer.id }).catch(() => {});
      });
      box.append(btn);
    } else {
      const note = el('div', 'note');
      const close = el('button', 'close', '×');
      close.type = 'button';
      close.setAttribute('aria-label', 'Dismiss');
      close.addEventListener('mousedown', (e) => { e.preventDefault(); dismissed.add(field); hide(); });
      note.append(el('span', '', 'Codemail is waiting for a code from this site…'), close);
      box.append(note);
    }
    host.style.display = 'block';
    place();
  }

  async function poll() {
    if (!field?.isConnected || Date.now() - started > GIVE_UP_MS) return hide();
    let res;
    try {
      res = await api.runtime.sendMessage({ type: 'codes' });
    } catch {
      return hide(); // the extension was reloaded or removed
    }
    if (!res || res.error) return hide(); // not connected: stay out of the way
    // Only codes from a verified sender that belongs to this site (or that
    // the user has linked to it) are offered here. Others stay in the popup.
    const offer = (res.messages || []).find((m) => m.code && m.match);
    render(offer || null);
  }

  document.addEventListener('focusin', (e) => {
    const el = e.target;
    if (!isCodeField(el) || dismissed.has(el) || el.value) return;
    field = el;
    started = Date.now();
    ensureWidget();
    clearInterval(timer);
    timer = setInterval(poll, POLL_MS);
    poll();
  }, true);

  document.addEventListener('focusout', () => {
    // Keep waiting briefly: the user may click away and back while the
    // email is in flight.
    setTimeout(() => {
      if (field && document.activeElement !== field && !boxesOf(field).includes(document.activeElement)) hide();
    }, 200);
  }, true);

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && host?.style.display === 'block') { dismissed.add(field); hide(); }
  }, true);
})();
