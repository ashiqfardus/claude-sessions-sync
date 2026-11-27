// The Conversations screen (Conversations.dc.html): filters, text search, a paged table,
// and Never delete or Delete from archive for the rows ticked. The filters live in the
// hash's query (#/conversations?project=...&page=2), so a reload or Back keeps them.
// Titles, names and paths come from the archive: they reach the page only through h().
import { h, icon, api, toast, confirmDialog, register, noArchive, when, GENERIC } from './app.js'
import { combo } from './combo.js'
import { pager } from './pager.js'

// Every filter as /api/conversations takes it, with its default. Defaults are left out of
// the hash and the request; limit 50 is the server's own default.
const DEFAULTS = { q: '', project: '', machine: '', age: '', state: '', look: '', role: '', since: '', until: '', case: '', regexp: '', all: '', limit: '50', page: '1' }
// The ones that narrow the list; role, case, regexp, all and limit only refine q.
const FILTERS = ['q', 'project', 'machine', 'age', 'state', 'look', 'since', 'until']
const OPTIONS = ['look', 'role', 'since', 'until', 'case', 'regexp', 'all', 'limit']
const CHECKS = ['case', 'regexp', 'all']

const pairs = (list) => list.map(([value, label]) => ({ value, label }))
const LISTS = {
  age: pairs([['', 'Any time'], ['7d', 'Last 7 days'], ['30d', 'Last 30 days'], ['over90d', 'Over 90 days ago'], ['over1y', 'Over a year ago']]),
  state: pairs([['', 'All'], ['compressed', 'Compressed'], ['kept', 'Never delete'], ['forked', 'Forked'], ['inuse', 'In use']]),
  look: pairs([['', 'The archive, every computer'], ['local', 'This computer only']]),
  role: pairs([['', 'Anyone'], ['user', 'You'], ['assistant', 'Claude']]),
  limit: pairs([['50', '50 matches'], ['200', '200 matches'], ['1000', '1,000 matches'], ['0', 'All matches']]),
}

// The mockup's badge icons, as path data.
const ZIP = ['M6 4h12a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z', 'M12 4v4', 'M12 10v2', 'M10 14h4v3h-4z']
const KEEP = ['M7 3h10v18l-5-4-5 4z']
const FORK = ['M4 5a2 2 0 1 0 4 0a2 2 0 1 0-4 0', 'M4 19a2 2 0 1 0 4 0a2 2 0 1 0-4 0', 'M16 8a2 2 0 1 0 4 0a2 2 0 1 0-4 0', 'M6 7v10', 'M18 10c0 4-6 3-11.5 7.5']

const number = (n) => n.toLocaleString('en-GB')
const plural = (n) => number(n) + ' conversation' + (n === 1 ? '' : 's')
const and = (names) => new Intl.ListFormat('en-GB', { type: 'conjunction' }).format(names)
const titleOf = (r) => r.title || 'Conversation ' + r.id.slice(0, 8)
const plain = (e) => (e && e.plain ? e.message : GENERIC)
// pathPart says whether a bucket or id can go into /read/ as it is: no separator, never "." or "..".
const pathPart = (s) => typeof s === 'string' && /^[A-Za-z0-9._-]+$/.test(s) && !/^\.+$/.test(s)

// size is the mockup's form: MB with one decimal, GB from 1 GB, KB below 0.05 MB.
function size(n) {
  if (n >= 2 ** 30) return (n / 2 ** 30).toFixed(1) + ' GB'
  if (n >= 0.05 * 2 ** 20) return (n / 2 ** 20).toFixed(1) + ' MB'
  return Math.ceil(n / 1024) + ' KB'
}

// lastMessage is "Today, 21:40", "Yesterday, 16:47", "18 Nov", or "18 Nov 2024" in an
// earlier year; "—" when unknown (Rulings P33, P43).
function lastMessage(t) {
  const s = when(t)
  if (!s) return '—'
  if (/^\d\d:\d\d$/.test(s)) return 'Today, ' + s
  const year = new Date(t).getFullYear()
  return year === new Date().getFullYear() || s.startsWith('Yesterday') ? s : s + ' ' + year
}

const read = (params) => Object.fromEntries(Object.entries(DEFAULTS).map(([k, v]) => [k, params.get(k) ?? v]))
const request = (state) => Object.fromEntries(Object.entries(state).filter(([k, v]) => v !== DEFAULTS[k]))

register('conversations', {
  async mount(el, params, signal) {
    const slot = (name) => el.querySelector(`[data-slot="${name}"]`)
    const state = read(params)
    const [first, projects, summary, retention] = await Promise.all([
      // A value the server refuses (a bad pattern, from a reload) shows on the screen, where it can be fixed.
      api.get('/api/conversations', request(state)).catch((e) => { if (e.status === 400) return { error: e }; throw e }),
      api.get('/api/projects').catch(() => null),
      api.get('/api/summary').catch(() => null), // the computers, and the archive's provider
      api.get('/api/retention').catch(() => null), // the computers deleting waits for
    ])
    if (first.problem) return first

    const provider = (summary && summary.archive && summary.archive.provider) || ''
    // The server's prune refuses while a computer holds out; when that can't be checked, so does the page.
    const unchecked = !retention || retention.problem || !Array.isArray(retention.holdouts)
    const holdouts = unchecked ? [] : retention.holdouts
    const blocked = unchecked || holdouts.length > 0
    // config keep refuses a retention.json it can't read: say so before the click, as Delete does.
    const keepPaused = !!(retention && retention.retentionUnreadable)
    const search = slot('q'), since = slot('since'), until = slot('until')
    const line = slot('summary'), fix = slot('fix'), rowsEl = slot('rows'), all = slot('select-all')
    const tableBox = rowsEl.closest('.table-box'), pages = slot('pages')
    const bar = slot('selection'), keepBtn = slot('keep'), delBtn = slot('delete')
    const selected = new Map() // id -> row, of the rows shown
    let shown = [], seq = 0, timer, busy = '' // busy: what the running action is doing, said in the bar

    // The combos. A value from an older link that no option has any more stays shown.
    const lists = {
      project: [{ value: '', label: 'All projects' }, ...(Array.isArray(projects) ? projects : []).map((p) => ({ value: p.key, label: p.name }))],
      machine: [{ value: '', label: 'Any computer' }, ...((summary && summary.computers) || []).map((c) => ({ value: c.name, label: c.name })), { value: '*many', label: 'More than one' }],
      ...LISTS,
    }
    const combos = {}
    for (const [key, list] of Object.entries(lists)) {
      const options = state[key] && !list.some((o) => o.value === state[key]) ? [...list, { value: state[key], label: state[key] }] : list
      combos[key] = combo(slot(key), { options, value: state[key], onChange: (v) => set({ [key]: v }) })
    }

    // sync puts the state into every control.
    function sync() {
      for (const k in combos) combos[k].value = state[k]
      search.value = state.q
      since.value = state.since
      until.value = state.until
      for (const k of CHECKS) slot(k).checked = state[k] === '1'
    }
    sync()
    el.querySelector('details').open = OPTIONS.some((k) => state[k] !== DEFAULTS[k])

    // save writes the state into the hash. replaceState keeps the screen mounted.
    let at = location.hash
    function save() {
      const s = new URLSearchParams(request(state)).toString()
      history.replaceState(null, '', '#/conversations' + (s ? '?' + s : ''))
      at = location.hash
    }
    function set(changes) {
      Object.assign(state, { page: '1' }, changes)
      save()
      load()
    }
    // The router remounts on a new hash, but not on its own last one (a rail click after
    // the filters changed): this catches that one. A remount aborts it first.
    addEventListener('hashchange', () => {
      if (location.hash === at || !location.hash.startsWith('#/conversations')) return
      Object.assign(state, read(new URLSearchParams(location.hash.split('?')[1] || '')))
      at = location.hash
      sync()
      load()
    }, { signal })

    const apply = () => { clearTimeout(timer); if (search.value !== state.q) set({ q: search.value }) }
    search.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(apply, 400) })
    search.addEventListener('keydown', (e) => { if (e.key === 'Enter') apply() })
    signal.addEventListener('abort', () => clearTimeout(timer))
    for (const input of [since, until]) input.addEventListener('change', () => set({ [input.dataset.slot]: input.value }))
    for (const k of CHECKS) slot(k).addEventListener('change', (e) => set({ [k]: e.target.checked ? '1' : '' }))

    async function load() {
      const n = ++seq
      if (state.q.trim()) line.textContent = 'Searching…'
      tableBox.setAttribute('aria-busy', 'true') // the rows stay, dimmed, until the new ones replace them: nothing jumps
      let view
      try {
        view = await api.get('/api/conversations', request(state))
      } catch (e) {
        view = { error: e }
      }
      if (n === seq && !signal.aborted) paint(view)
    }

    function paint(view) {
      selected.clear()
      fix.replaceChildren()
      tableBox.removeAttribute('aria-busy')
      shown = (view.rows) || []
      tableBox.hidden = pages.hidden = !shown.length
      if (view.error) {
        line.textContent = plain(view.error)
        if (view.error.status !== 400) fix.append(h('button', { type: 'button', class: 'btn', onclick: load }, 'Try again'))
      } else if (view.problem) {
        line.textContent = ''
        fix.append(noArchive(view.problem, load))
      } else if (!view.total && FILTERS.some((k) => state[k] !== DEFAULTS[k])) {
        line.textContent = 'No conversations match these filters.'
        fix.append(h('button', { type: 'button', class: 'btn', onclick: clear }, 'Clear filters'))
      } else if (!view.total) {
        line.replaceChildren('No conversations in the archive yet. Back up now on the ', h('a', { href: '#/overview' }, 'Overview page'), '.')
      } else {
        const limit = Number(state.limit)
        line.textContent = `${plural(view.total)}, ${size(view.totalBytes)}. ${number(shown.length)} shown, newest first.` +
          (state.q.trim() && view.stopped && limit ? ` Stopped after ${number(limit)} matches; older conversations were not searched.` : '')
        if (String(view.page) !== state.page) { state.page = String(view.page); save() } // past the last page: the server gave the last
        const from = (view.page - 1) * view.per
        slot('range').textContent = `${number(from + 1)} to ${number(from + shown.length)} of ${number(view.total)}`
        pager(slot('pager'), { page: view.page, pages: view.pages, onGo: (p) => { state.page = String(p); save(); load() } })
      }
      if (!view.error && !view.problem && view.unreadable > 0) line.append(` ${plural(view.unreadable)} couldn't be read, so results may be incomplete.`)
      rowsEl.replaceChildren(...shown.map(row))
      mark()
    }

    function clear() {
      Object.assign(state, DEFAULTS)
      sync()
      save()
      load()
    }

    function row(r) {
      const title = titleOf(r)
      const badge = (cls, paths, text) => h('span', { class: 'badge ' + cls }, icon(12, ...paths), text)
      const bucket = (r.buckets || [])[0]
      return h('tr', null,
        h('td', null, h('label', { class: 'tick' }, h('input', { type: 'checkbox', class: 'check', 'aria-label': 'Select: ' + title, onchange: (e) => tick(r, e.target.checked) }))),
        h('td', null, h('span', { class: 'title-cell' }, h('strong', null, title), h('code', { class: 'muted' }, r.id.slice(0, 8)))),
        h('td', null, r.project),
        h('td', null, lastMessage(r.updated)),
        h('td', null, (r.machines || []).join(', ')),
        h('td', { class: 'r' }, size(r.size), r.compressed && h('span', { class: 'muted small block' }, size(r.full) + ' uncompressed')),
        h('td', null, h('span', { class: 'row gap-5' },
          r.compressed && badge('', ZIP, 'Compressed'),
          r.kept && badge('green', KEEP, 'Kept'),
          r.forked && badge('amber', FORK, 'Forked'),
          r.inUse && h('span', { class: 'muted small' }, 'In use'))),
        h('td', { class: 'r' }, r.readable && pathPart(bucket) && pathPart(r.id)
          ? h('a', { class: 'btn sm ghost', href: '/read/' + bucket + '/' + r.id, target: '_blank', rel: 'noopener', 'aria-label': 'Read: ' + title }, 'Read')
          : [h('button', { type: 'button', class: 'btn sm ghost', disabled: true, 'aria-label': `Read: ${title} (no page yet)` }, 'Read'), h('span', { class: 'muted small block' }, 'No page yet')]))
    }

    function tick(r, on) {
      if (on) selected.set(r.id, r)
      else selected.delete(r.id)
      mark()
    }

    // mark shows the selection: the ticks, the rows' tint, and the dark bar.
    function mark() {
      const n = selected.size
      shown.forEach((r, i) => {
        const tr = rowsEl.children[i], on = selected.has(r.id)
        tr.classList.toggle('sel', on)
        tr.querySelector('input').checked = on
      })
      all.checked = n > 0 && n === shown.length
      all.indeterminate = n > 0 && n < shown.length
      let bytes = 0
      for (const r of selected.values()) bytes += r.size
      const text = busy || (n ? `${number(n)} selected, ${size(bytes)}` : '')
      if (n) slot('selected').textContent = text
      slot('selected-say').textContent = text // a live region outside the bar, which is hidden until now
      bar.hidden = !n
      keepBtn.disabled = !!busy || keepPaused
      delBtn.disabled = !!busy || blocked
    }

    all.addEventListener('change', () => {
      if (all.checked) for (const r of shown) selected.set(r.id, r)
      else selected.clear()
      mark()
    })
    slot('clear').addEventListener('click', () => { selected.clear(); mark(); all.focus() })
    if (blocked) {
      slot('wait-text').textContent = unchecked ? "Deleting is paused: the other computers can't be checked right now."
        : `Deleting waits until ${and(holdouts)} ${holdouts.length === 1 ? 'updates' : 'update'}.`
      slot('wait').hidden = false
      delBtn.setAttribute('aria-describedby', 'conv-wait')
    }
    if (keepPaused) {
      slot('keep-unread').hidden = false
      keepBtn.setAttribute('aria-describedby', 'conv-keep-unread')
    }

    // After an action the bar is gone: focus goes to the control after it.
    const refocus = () => { if (!el.contains(document.activeElement) || bar.contains(document.activeElement)) (tableBox.hidden ? el : all).focus() }

    // Add to Never delete keeps each one in turn, as `config keep <id>` does. Each request
    // waits for its job, so the list finishes even if the screen goes meanwhile.
    keepBtn.addEventListener('click', async () => {
      const rows = [...selected.values()]
      busy = `Adding ${plural(rows.length)} to Never delete…`
      mark()
      let added = 0, err = ''
      for (const r of rows) {
        try {
          const st = await api.run('config', { action: 'keep', args: [r.id] }, { wait: true })
          if (!st.ok) { err = st.error || GENERIC; break }
          added++
        } catch (e) {
          err = plain(e)
          break
        }
      }
      busy = ''
      const said = added ? `Added ${plural(added)} to Never delete` : ''
      toast(said && err ? said + '. ' + err : said || err)
      if (signal.aborted) return
      await load()
      refocus()
    })

    delBtn.addEventListener('click', async () => {
      const rows = [...selected.values()], ids = rows.map((r) => r.id), kept = rows.filter((r) => r.kept).length
      const title = 'Delete ' + plural(ids.length)
      // Provider wording only when the provider is known (Ruling P25).
      const body = `They are removed from the archive in ${provider || 'your sync folder'}. Your computers keep their own copies` +
        (provider === 'Google Drive' ? ', and Google Drive keeps the files in its trash for 30 days.' : '.') +
        (kept ? ` ${number(kept)} of these ${kept === 1 ? 'is' : 'are'} on Never delete and will be skipped.` : '')
      const items = rows.map((r) => titleOf(r) + (r.kept ? ' (Never delete, skipped)' : ''))
      if (!await confirmDialog({ title, body, items, confirm: title, danger: true })) return
      busy = `Deleting ${plural(ids.length)}…`
      mark()
      let st
      try {
        st = await api.run('prune', { ids, yes: true }, { wait: true })
      } catch (e) {
        st = { ok: false, error: plain(e) }
      }
      busy = ''
      // A run that fails partway still says what it deleted (Ruling P44).
      const res = st.result || {}, n = res.deleted || 0, skipped = (res.plan && res.plan.kept) || 0
      let text = `Deleted ${plural(n)} from the archive` + (skipped ? `. ${plural(skipped)} on Never delete ${skipped === 1 ? 'was' : 'were'} skipped` : '')
      if (!st.ok) text = (n ? text + '. ' : '') + (st.error || GENERIC)
      if (!signal.aborted) await load()
      toast(text, n ? { action: { label: 'Restore', run: () => restore(ids) } } : undefined)
      // The toast is last in the page: put focus on Restore, so the keyboard can reach it.
      const undo = n && document.querySelector('#toasts .toast button')
      if (undo && !signal.aborted) undo.focus()
      else if (!signal.aborted) refocus()
    })

    async function restore(ids) {
      let st
      try {
        st = await api.run('prune-undo', { ids }, { wait: true })
      } catch (e) {
        st = { ok: false, error: plain(e) }
      }
      if (!st.ok) toast(st.error || GENERIC)
      else toast(`Restored ${plural(st.result && Array.isArray(st.result.undone) ? st.result.undone.length : ids.length)}. Each comes back at the next backup of a computer that still has it.`)
      if (signal.aborted) return
      if (st.ok) await load()
      // Focus left with the toast's Restore button: it goes to the list, not the page.
      if (document.activeElement === el || document.activeElement === document.body) (tableBox.hidden ? el.querySelector('h1') : all).focus()
    }

    paint(first)
    return first
  },
})
