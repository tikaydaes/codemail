// Renders the Markdown the server produces for message bodies. It builds
// DOM nodes directly and never parses HTML, so message content cannot
// inject markup; anything it does not recognise is shown as plain text.
import { h } from '/lib.js';

const fence = /^\s*(```|~~~)/;
const heading = /^(#{1,6})\s+(.*?)\s*#*\s*$/;
const rule = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
const quote = /^ {0,3}>\s?/;
const marker = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;

const startsBlock = (line) => fence.test(line) || heading.test(line) || rule.test(line) || quote.test(line) || marker.test(line);

export function renderMarkdown(source) {
  return h('div', { class: 'md' }, blocks((source || '').replace(/\r\n?/g, '\n').split('\n')));
}

function blocks(lines) {
  const out = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    let m;
    if (!line.trim()) {
      i++;
    } else if ((m = fence.exec(line))) {
      const body = [];
      for (i++; i < lines.length && !lines[i].trim().startsWith(m[1]); i++) body.push(lines[i]);
      i++;
      out.push(h('pre', {}, h('code', {}, body.join('\n'))));
    } else if ((m = heading.exec(line))) {
      // The page already has its own h1 and h2, so message headings start lower.
      out.push(h('h' + Math.min(6, m[1].length + 2), {}, inline(m[2])));
      i++;
    } else if (rule.test(line)) {
      out.push(h('hr', {}));
      i++;
    } else if (quote.test(line)) {
      const body = [];
      for (; i < lines.length && quote.test(lines[i]); i++) body.push(lines[i].replace(quote, ''));
      out.push(h('blockquote', {}, blocks(body)));
    } else if ((m = marker.exec(line))) {
      const indent = m[1].length;
      const ordered = /\d/.test(m[2]);
      const items = [];
      while (i < lines.length) {
        const mm = marker.exec(lines[i]);
        if (!mm || mm[1].length !== indent || /\d/.test(mm[2]) !== ordered) break;
        // An item runs until the next marker at this depth, or a line
        // that is neither blank nor indented.
        const width = indent + mm[2].length + 1;
        const body = [mm[3]];
        for (i++; i < lines.length; i++) {
          const l = lines[i];
          const next = marker.exec(l);
          if (next && next[1].length <= indent) break;
          if (l.trim() && !/^\s/.test(l)) break;
          if (!l.trim() && !(lines[i + 1] || '').startsWith(' '.repeat(indent + 1))) break;
          body.push(l.slice(Math.min(width, l.length - l.trimStart().length)));
        }
        const kids = blocks(body);
        // An item that is one paragraph, optionally followed by nested
        // lists, reads better without the paragraph spacing.
        const [first, ...rest] = kids;
        const tight = first && first.tagName === 'P' && rest.every((k) => k.tagName === 'UL' || k.tagName === 'OL');
        items.push(h('li', {}, tight ? [...first.childNodes, ...rest] : kids));
        while (i < lines.length && !lines[i].trim()) i++;
      }
      out.push(h(ordered ? 'ol' : 'ul', {}, items));
    } else {
      const body = [line];
      for (i++; i < lines.length && lines[i].trim() && !startsBlock(lines[i]); i++) body.push(lines[i]);
      out.push(h('p', {}, inline(body.join('\n'))));
    }
  }
  return out;
}

// One alternative per inline construct, tried at the earliest position.
const token = new RegExp([
  /\\([\\`*_{}[\]()#+\-.!<>|~])/,                                     // 1 escaped character
  /`([^`\n]+)`/,                                                       // 2 code
  /!\[([^\]]*)\]\([^)\n]*\)/,                                          // 3 image: its text only
  /\[((?:\\.|[^\]\\])*)\]\(\s*<?([^\s)>]+)>?(?:\s+"[^"]*")?\s*\)/,     // 4,5 link
  /<((?:https?:\/\/|mailto:)[^\s>]+)>/,                                // 6 bare link
  /\*\*([^\n]+?)\*\*|__([^\n]+?)__/,                                   // 7,8 strong
  /~~([^\n]+?)~~/,                                                     // 9 struck through
  /\*([^\s*][^\n*]*?)\*|\b_([^\s_][^\n_]*?)_\b/,                       // 10,11 emphasis
  / {2,}\n|\\\n/,                                                      // line break
].map((r) => r.source).join('|'), 'g');

function inline(text) {
  const out = [];
  let last = 0;
  const plain = (s) => { if (s) out.push(s.replace(/\n/g, ' ')); };
  for (const m of text.matchAll(token)) {
    plain(text.slice(last, m.index));
    last = m.index + m[0].length;
    if (m[1] !== undefined) out.push(m[1]);
    else if (m[2] !== undefined) out.push(h('code', {}, m[2]));
    else if (m[3] !== undefined) plain(m[3]);
    else if (m[4] !== undefined) out.push(link(m[5], inline(m[4])));
    else if (m[6] !== undefined) out.push(link(m[6], [m[6].replace(/^mailto:/, '')]));
    else if (m[7] !== undefined || m[8] !== undefined) out.push(h('strong', {}, inline(m[7] ?? m[8])));
    else if (m[9] !== undefined) out.push(h('s', {}, inline(m[9])));
    else if (m[10] !== undefined || m[11] !== undefined) out.push(h('em', {}, inline(m[10] ?? m[11])));
    else out.push(h('br', {}));
  }
  plain(text.slice(last));
  return out;
}

// Links open in a new tab and show where they go. Anything that is not a
// web, mail or phone link is shown as its text only.
function link(href, kids) {
  let url;
  try { url = new URL(href); } catch { return kids; }
  if (!['http:', 'https:', 'mailto:', 'tel:'].includes(url.protocol)) return kids;
  if (!kids.length) kids = [url.hostname || href];
  return h('a', { href: url.href, target: '_blank', rel: 'noopener noreferrer nofollow', title: url.hostname ? `Opens ${url.hostname}` : href }, kids);
}
