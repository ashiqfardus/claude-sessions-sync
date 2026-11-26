// The command map page (Commands.dc.html): every CLI command and option, and where it is
// in the panel, from GET /api/commands (Go's commandMap, which a test checks against the
// CLI). Each group is a tbody of its own, so its scope=rowgroup heading names only its rows.
import { h, api, register } from './app.js'

// Screens a row names after "also" or "on", linked as the mockup links them.
const NAMED = { Settings: 'settings', Conversations: 'conversations', 'Bring in': 'import' }
const LATER = /\b(also|on) (Settings|Conversations|Bring in)\b/

function linked(text) {
  const out = []
  for (let m; (m = LATER.exec(text));) {
    const at = m.index + m[1].length + 1
    out.push(text.slice(0, at), h('a', { href: '#/' + NAMED[m[2]] }, m[2]))
    text = text.slice(at + m[2].length)
  }
  return [...out, text]
}

// place is the row's words. The screen's name, up to the first comma, links to it; a
// row without a screen is terminal only.
function place(r) {
  const text = String(r.place || '')
  if (!/^[a-z]+$/.test(r.screen || '')) return h('td', { class: 'muted' }, text)
  const i = text.indexOf(', ')
  const [name, rest] = i < 0 ? [text, ''] : [text.slice(0, i), text.slice(i)]
  return h('td', null, h('a', { href: '#/' + r.screen }, name), ...linked(rest))
}

register('commands', {
  async mount(el) {
    const rows = await api.get('/api/commands')
    const groups = []
    for (const r of Array.isArray(rows) ? rows : []) {
      let g = groups[groups.length - 1]
      if (!g || g.name !== r.group) {
        g = { name: r.group, body: h('tbody', null, h('tr', null, h('th', { scope: 'rowgroup', colspan: 2 }, r.group))) }
        groups.push(g)
      }
      g.body.append(h('tr', null, h('td', null, (r.terminal || []).flatMap((t, i) => [i ? ', ' : '', h('code', null, t)])), place(r)))
    }
    const body = groups.length ? groups.map((g) => g.body)
      : [h('tbody', null, h('tr', null, h('td', { colspan: 2, class: 'muted' }, "The command list can't be shown right now. Reload the page to try again.")))]
    el.querySelector('[data-slot="rows"]').replaceWith(...body)
  },
})
