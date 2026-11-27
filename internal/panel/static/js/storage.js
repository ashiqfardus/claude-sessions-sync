// The Storage screen (Storage.dc.html): the usage bar, three ways to free space
// (compress, duplicate copies, delete old conversations), Never delete and the deleted
// list in summary, and the biggest projects. Project names and computer names come from
// the archive: they reach the page only through h() and textContent.
import { h, api, follow, toast, confirmDialog, register, when, GENERIC } from './app.js'

const number = (n) => n.toLocaleString('en-GB')
const plural = (n, word) => number(n) + ' ' + word + (n === 1 ? '' : 's')
const copies = (n) => (n === 1 ? '1 copy' : number(n) + ' copies')
const dups = (n) => number(n) + ' duplicate ' + (n === 1 ? 'copy' : 'copies')
// What a line that reads as "nothing" adds when some of the archive couldn't be read.
const PARTIAL = "Some of the archive couldn't be read right now, so this may be incomplete."
const and = (names) => new Intl.ListFormat('en-GB', { type: 'conjunction' }).format(names)
const plain = (e) => (e && e.plain ? e.message : GENERIC)
// refocus puts focus back on btn when disabling a button dropped it to the page.
const refocus = (btn) => { if (document.activeElement === document.body && btn.isConnected && !btn.hidden) btn.focus() }
// sized is the Overview's form, for every size here: 4.8 GB, 230 MB, 12 KB.
function sized(b) {
  for (const [unit, n, d] of [['GB', 2 ** 30, 1], ['MB', 2 ** 20, 0], ['KB', 2 ** 10, 0]]) if (b >= n) return (b / n).toFixed(d) + ' ' + unit
  return b + ' B'
}
// frees shows "Frees about 12 MB" beside a way's title, or nothing when b is 0.
function frees(badge, b) {
  badge.textContent = b > 0 ? 'Frees about ' + sized(b) : ''
  badge.hidden = !(b > 0)
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
      const partial = v.unreadable > 0
      slot('prune-line').textContent = v.keepDays ? `Deletes conversations not used for ${ageLabel(v.keepDays)}.` : 'Not set up.' + (partial ? ' ' + PARTIAL : '')

      const kept = v.kept.projects, parts = []
      if (kept.length === 1) parts.push(`the ${kept[0]} project`)
      else if (kept.length > 1 && kept.length < 4) parts.push(`the ${and(kept)} projects`)
      else if (kept.length) parts.push(plural(kept.length, 'project'))
      if (v.kept.conversations) parts.push(plural(v.kept.conversations, 'conversation'))
      slot('kept-line').textContent = 'Never delete: ' + (parts.length ? parts.join(' and ') : 'nothing yet') + (partial ? '. ' + PARTIAL : '')
      const { count, here } = v.deleted
      slot('deleted-line').textContent = 'Deleted recently: ' + (!count ? 'none'
        : plural(count, 'conversation') + (here === count ? ', still on this computer' : here ? `, ${number(here)} still on this computer` : ''))
      slot('restore-link').hidden = !count

      // Bars compare the named projects; the row of all the others (key "") gets none, or
      // its total would dwarf them.
      const top = Math.max(1, ...v.projects.filter((p) => p.key).map((p) => p.bytes))
      slot('projects').closest('.table-box').hidden = !v.projects.length
      slot('no-projects').hidden = !!v.projects.length
      slot('projects').replaceChildren(...v.projects.map((p, i) => {
        const fill = h('span')
        fill.style.width = (100 * p.bytes) / top + '%'
        fill.style.animationDelay = 150 + i * 70 + 'ms'
        return h('tr', null,
          h('td', null, p.key ? h('a', { href: '#/conversations?project=' + encodeURIComponent(p.key) }, p.name) : p.name),
          h('td', { class: 'r' }, number(p.conversations)),
          h('td', null, h('span', { class: 'row space-cell' }, p.key ? h('span', { class: 'bar space-bar' }, fill) : h('span', { class: 'bar space-bar no-bar', 'aria-hidden': 'true' }), h('span', { class: 'space-size' }, sized(p.bytes)))))
      }))
    }

    function paintZip() {
      zip.checked = v.zipOn
      slot('zip-state').textContent = v.zipOn ? 'On' : 'Off'
      frees(slot('zip-frees'), v.zipFrees) // the amount is beside the title, so the line need not say it
      const line = slot('zip-line'), some = v.zipFrees > 0
      line.textContent = v.zipOn ? (some ? '' : 'Nothing is old enough to compress yet.') : (some ? 'Off. Turn it on to free that space.' : 'Off.')
      line.hidden = !line.textContent
      const waits = v.zipWaitsFor
      slot('zip-wait-text').textContent = waits.length ? `Starts once ${and(waits)} ${waits.length === 1 ? 'updates' : 'update'}.` : ''
      slot('zip-wait').hidden = !v.zipOn || !waits.length
    }

    // paintDup shows this screen's last check, or else the panel's (v.duplicates).
    function paintDup() {
      check.textContent = busy === 'check' ? 'Checking' : 'Check now'
      check.disabled = remove.disabled = !!busy
      if (busy) return frees(slot('dup-frees'), 0)
      const last = v.duplicates
      const n = plan ? plan.remove.length : last ? last.copies : 0
      const bytes = plan ? plan.remove.reduce((s, c) => s + c.size, 0) : last ? last.bytes : 0
      // Waiting copies and an unread manifest: the CLI's notes, in plain words.
      const waiting = plan ? Object.keys(plan.waiting || {}).sort() : []
      if (!plan && !last) dupLine.textContent = 'Not checked yet.'
      else if (!n) dupLine.textContent = `None ${waiting.length ? 'can be removed yet' : 'found'}. Checked ${checked(plan ? plan.at : last.at)}.`
      else dupLine.textContent = `Found ${dups(n)}.`
      frees(slot('dup-frees'), plan && plan.unread ? 0 : n ? bytes : 0) // nothing frees while a list can't be read
      remove.hidden = !n
      remove.textContent = 'Remove ' + copies(n)
      let note = ''
      if (plan && plan.unread) {
        note = (n ? "Nothing can be removed while a computer's list in the archive can't be read."
          : "A computer's list in the archive can't be read, so this may be incomplete.") + ' Let your sync app finish, then check again.'
        remove.disabled = true
      } else if (waiting.length) {
        note = `${sized(plan.waitingSize)} more is kept for now, because ${and(waiting)} would upload those copies again. Check again once ${waiting.length === 1 ? 'it has' : 'they have'} updated and backed up.`
      }
      slot('dup-wait-text').textContent = note
      dupWait.hidden = !note
    }

    async function reload() {
      try { v = await api.get('/api/storage') } catch (e) { return toast(plain(e)) }
      if (!signal.aborted && !v.problem) paint()
    }

    // runClean runs clean and gives the job's final state. A check streams its counter
    // into the line and gives null once the screen has gone. A removal is held to its end
    // instead, so its result is told even after the screen has gone.
    async function runClean(yes) {
      busy = yes ? 'remove' : 'check'
      paintDup()
      dupLine.setAttribute('aria-busy', 'true') // the counter is read out once, when it is done
      dupLine.textContent = yes ? 'Removing duplicate copies.' : comparing()
      let st
      try {
        if (yes) st = await api.run('clean', { yes }, { wait: true })
        else {
          const { id } = await api.run('clean', { yes })
          st = await follow(id, { signal, onProgress: (text) => { dupLine.textContent = comparing(text) } })
        }
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
      plan = st.ok && st.result ? { ...st.result.plan, at: new Date().toISOString() } : null
      paintDup()
      if (!st.ok) dupLine.textContent = st.error || GENERIC
      return st
    }

    check.addEventListener('click', async () => {
      if (await compare()) refocus(check)
    })

    remove.addEventListener('click', async () => {
      if (busy) return
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
      if (busy || signal.aborted) return // a check started meanwhile
      if (!await confirmDialog({ title: label + '?', body, items: [...groups].map(([name, g]) => `${name}: ${copies(g.n)}, ${sized(g.bytes)}`), confirm: label, danger: true })) return
      const st = await runClean(true)
      // A run that fails partway still says what it removed (Ruling P44). clean compares
      // again before removing, so it may find other copies than the dialog listed.
      const res = st.result || {}, done = res.deleted || 0
      let text = `Removed ${dups(done)} and freed ${sized(res.freed || 0)}`
      if (st.ok && done !== n) text += `. The check found ${number(n)}: the archive changed since`
      if (!st.ok) text = (done ? text + '. ' : '') + (st.error || GENERIC)
      toast(text)
      plan = null
      if (signal.aborted) return
      await reload()
      refocus(check)
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
