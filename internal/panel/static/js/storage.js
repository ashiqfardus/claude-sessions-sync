// The Storage screen (Storage.dc.html): the usage bar, three ways to free space
// (compress, duplicate copies, delete old conversations), Never delete and the deleted
// list in summary, and the biggest projects. Project names and computer names come from
// the archive: they reach the page only through h() and textContent.
import { h, api, follow, toast, confirmDialog, register, when, GENERIC } from './app.js'

const number = (n) => n.toLocaleString('en-GB')
const plural = (n, word) => number(n) + ' ' + word + (n === 1 ? '' : 's')
const copies = (n) => (n === 1 ? '1 copy' : number(n) + ' copies')
const and = (names) => new Intl.ListFormat('en-GB', { type: 'conjunction' }).format(names)
const plain = (e) => (e && e.plain ? e.message : GENERIC)
// refocus puts focus back on btn when disabling a button dropped it to the page.
const refocus = (btn) => { if (document.activeElement === document.body && btn.isConnected && !btn.hidden) btn.focus() }
// The CLI's own form for what clean and prune free: MB with one decimal.
const mb = (n) => (n / 2 ** 20).toFixed(1) + ' MB'
// sized is the Overview's form: 4.8 GB, 230 MB, 12 KB.
function sized(b) {
  for (const [unit, n, d] of [['GB', 2 ** 30, 1], ['MB', 2 ** 20, 0], ['KB', 2 ** 10, 0]]) if (b >= n) return (b / n).toFixed(d) + ' ' + unit
  return b + ' B'
}
// The age rule's choices, as Delete offers them.
const AGES = { 90: '3 months', 180: '6 months', 365: '1 year', 730: '2 years' }
const ageLabel = (days) => AGES[days] || plural(days, 'day')

// checked is "just now", "today at 22:10", "yesterday at 18:30" or "on 18 Nov".
function checked(t) {
  const s = when(t)
  if (!s) return ''
  if (Date.now() - new Date(t) < 60000) return 'just now'
  if (/^\d\d:\d\d$/.test(s)) return 'today at ' + s
  if (s.startsWith('Yesterday, ')) return 'yesterday at ' + s.slice(11)
  return 'on ' + s
}

// progress turns clean's "comparing copies: n/m" into words.
function comparing(text) {
  const m = /(\d+)\/(\d+)/.exec(text || '')
  return m ? `Comparing copies of ${plural(Number(m[2]), 'conversation')}: ${number(Number(m[1]))} done.` : 'Comparing copies.'
}

register('storage', {
  async mount(el, params, signal) {
    const slot = (name) => el.querySelector(`[data-slot="${name}"]`)
    const first = await api.get('/api/storage')
    if (first.problem) return first

    const zip = slot('zip'), check = slot('check'), remove = slot('remove')
    const dupLine = slot('dup-line'), dupWait = slot('dup-wait')
    let v = first, plan = null, busy = false // plan: this screen's last duplicates check

    function paint() {
      const { conversations, work, other } = v.sizes
      const total = conversations + work + other
      const provider = v.archive && v.archive.provider // Ruling P25
      slot('usage').replaceChildren('Your archive uses ', h('strong', { class: 'num' }, sized(total)), provider ? ' of ' + provider + '.' : ' in your sync folder.',
        v.unreadable > 0 ? " Some of it couldn't be read, so these numbers may be low." : '')
      const bar = slot('bar')
      for (const [part, b] of [['conversations', conversations], ['work', work], ['other', other]]) {
        bar.querySelector('.part-' + part).style.width = (total ? (100 * b) / total : 0) + '%'
        slot(part + '-size').textContent = sized(b)
      }
      bar.setAttribute('aria-label', `Conversations ${sized(conversations)}, work folders ${sized(work)}, pages and other files ${sized(other)}`)

      paintZip()
      paintDup()
      slot('prune-line').textContent = v.keepDays ? `Deletes conversations not used for ${ageLabel(v.keepDays)}.` : 'Not set up.'

      const kept = v.kept.projects, parts = []
      if (kept.length === 1) parts.push(`the ${kept[0]} project`)
      else if (kept.length > 1 && kept.length < 4) parts.push(`the ${and(kept)} projects`)
      else if (kept.length) parts.push(plural(kept.length, 'project'))
      if (v.kept.conversations) parts.push(plural(v.kept.conversations, 'conversation'))
      slot('kept-line').textContent = 'Never delete: ' + (parts.length ? parts.join(' and ') : 'nothing yet')
      const { count, here } = v.deleted
      slot('deleted-line').textContent = 'Deleted recently: ' + (!count ? 'none'
        : plural(count, 'conversation') + (here === count ? ', still on this computer' : here ? `, ${number(here)} still on this computer` : ''))
      slot('restore-link').hidden = !count

      const top = Math.max(1, ...v.projects.map((p) => p.bytes))
      slot('projects').closest('.table-box').hidden = !v.projects.length
      slot('no-projects').hidden = !!v.projects.length
      slot('projects').replaceChildren(...v.projects.map((p, i) => {
        const fill = h('span')
        fill.style.width = (100 * p.bytes) / top + '%'
        fill.style.animationDelay = 150 + i * 70 + 'ms'
        return h('tr', null,
          h('td', null, p.key ? h('a', { href: '#/conversations?project=' + encodeURIComponent(p.key) }, p.name) : p.name),
          h('td', { class: 'r' }, number(p.conversations)),
          h('td', null, h('span', { class: 'row space-cell' }, h('span', { class: 'bar space-bar' }, fill), h('span', { class: 'space-size' }, sized(p.bytes)))))
      }))
    }

    function paintZip() {
      zip.checked = v.zipOn
      slot('zip-state').textContent = v.zipOn ? 'On' : 'Off'
      const frees = v.zipFrees > 0 ? sized(v.zipFrees) : ''
      slot('zip-line').textContent = v.zipOn ? (frees ? `Frees about ${frees}.` : 'Nothing is old enough to compress yet.')
        : (frees ? `Off. Turning it on frees about ${frees}.` : 'Off.')
      const waits = v.zipWaitsFor
      slot('zip-wait-text').textContent = waits.length ? `Starts once ${and(waits)} ${waits.length === 1 ? 'updates' : 'update'}.` : ''
      slot('zip-wait').hidden = !v.zipOn || !waits.length
    }

    // paintDup shows this screen's last check, or else the panel's (v.duplicates).
    function paintDup() {
      check.textContent = busy === 'check' ? 'Checking' : 'Check now'
      check.disabled = remove.disabled = !!busy
      if (busy) return
      const last = v.duplicates
      const n = plan ? plan.remove.length : last ? last.copies : 0
      const bytes = plan ? plan.remove.reduce((s, c) => s + c.size, 0) : last ? last.bytes : 0
      // Waiting copies and an unread manifest: the CLI's notes, in plain words.
      const waiting = plan ? Object.keys(plan.waiting || {}).sort() : []
      if (!plan && !last) dupLine.textContent = 'Not checked yet.'
      else if (!n) dupLine.textContent = `None ${waiting.length ? 'can be removed yet' : 'found'}. Checked ${checked(plan ? plan.at : last.at)}.`
      else dupLine.textContent = `${number(n)} duplicate ${n === 1 ? 'copy' : 'copies'}, ${mb(bytes)}.`
      remove.hidden = !n
      remove.textContent = 'Remove ' + copies(n)
      let note = ''
      if (plan && plan.unread && n) {
        note = "Nothing can be removed while a computer's list in the archive can't be read. Let your sync app finish, then check again."
        remove.disabled = true
      } else if (waiting.length) {
        note = `${mb(plan.waitingSize)} more is kept for now, because ${and(waiting)} would upload those copies again. Check again once ${waiting.length === 1 ? 'it has' : 'they have'} updated and backed up.`
      }
      slot('dup-wait-text').textContent = note
      dupWait.hidden = !note
    }

    async function reload() {
      try { v = await api.get('/api/storage') } catch (e) { return toast(plain(e)) }
      if (!signal.aborted && !v.problem) paint()
    }

    // runClean runs clean, preview or not, streaming its counter into the line. It gives
    // the job's final state, or null once the screen has gone.
    async function runClean(yes) {
      busy = yes ? 'remove' : 'check'
      paintDup()
      dupLine.setAttribute('aria-busy', 'true') // the counter is read out once, when it is done
      dupLine.textContent = comparing()
      let st
      try {
        const { id } = await api.run('clean', { yes })
        st = await follow(id, { signal, onProgress: (text) => { dupLine.textContent = comparing(text) } })
      } catch (e) {
        st = { ok: false, error: plain(e) }
      }
      busy = false
      dupLine.removeAttribute('aria-busy')
      return st
    }

    // compare is Check now: clean without yes. It gives the job's state, null once gone.
    async function compare() {
      const st = await runClean(false)
      if (!st) return null
      if (st.ok && st.result) plan = { ...st.result.plan, at: new Date().toISOString() }
      paintDup()
      if (!st.ok) dupLine.textContent = st.error || GENERIC
      return st
    }

    check.addEventListener('click', async () => {
      if (await compare()) refocus(check)
    })

    remove.addEventListener('click', async () => {
      if (!plan) { // the count came from an earlier check: compare again before asking
        const st = await compare()
        if (!st) return
        if (!st.ok || !plan || !plan.remove.length || plan.unread) return refocus(remove.hidden ? check : remove)
        refocus(remove)
      }
      const list = plan.remove, n = list.length
      // The dialog lists the projects the copies are in, by name where the archive has one.
      const projects = await api.get('/api/projects').catch(() => null)
      const names = new Map()
      if (Array.isArray(projects)) for (const p of projects) for (const b of p.buckets || []) names.set(b, p.name)
      const groups = new Map()
      for (const c of list) {
        const name = names.get(c.bucket) || c.bucket, g = groups.get(name) || { n: 0, bytes: 0 }
        g.n++
        g.bytes += c.size
        groups.set(name, g)
      }
      const provider = v.archive && v.archive.provider
      const body = 'Each conversation stays in the archive, in its other copy.' +
        (provider === 'Google Drive' ? ' Google Drive keeps deleted files in its trash for 30 days.' : '')
      const label = 'Remove ' + copies(n)
      if (!await confirmDialog({ title: label + '?', body, items: [...groups].map(([name, g]) => `${name}: ${copies(g.n)}, ${mb(g.bytes)}`), confirm: label, danger: true })) return
      const st = await runClean(true)
      if (!st) return
      // A run that fails partway still says what it removed (Ruling P44).
      const res = st.result || {}, done = res.deleted || 0
      let text = `Removed ${done} duplicate ${done === 1 ? 'copy' : 'copies'} and freed ${mb(res.freed || 0)}`
      if (!st.ok) text = (done ? text + '. ' : '') + (st.error || GENERIC)
      toast(text)
      plan = null
      await reload()
      if (!signal.aborted) refocus(check)
    })

    zip.addEventListener('change', async () => {
      const on = zip.checked
      zip.disabled = true
      let err = ''
      try {
        const st = await api.run('config', { action: 'zip-sessions', args: [on ? 'on' : 'off'] }, { wait: true })
        if (!st.ok) err = st.error || GENERIC
      } catch (e) {
        err = plain(e)
      }
      zip.disabled = false
      if (err) toast(err)
      else v.zipOn = on
      paintZip()
      refocus(zip)
    })

    paint()
    return first
  },
})
