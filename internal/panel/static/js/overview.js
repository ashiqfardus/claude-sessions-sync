// The Overview (Main.dc.html): the archive and its computers, Back up now and Preview
// backup, health, storage and recent activity. Archive text (computer names, paths,
// log lines) reaches the page only as text, through h().
import { h, icon, api, follow, toast, modal, register, noArchive, when, reducedMotion, GENERIC } from './app.js'

const RING = 'M3 12a9 9 0 1 0 18 0a9 9 0 1 0-18 0'
const ICON = {
  ok: ['M5 12.5l4.5 4.5L19 7.5'],
  clock: [RING, 'M12 7v5l3 2'],
  warn: ['M12 4l9 16H3z', 'M12 10v4', 'M12 17.5v.01'],
  fail: [RING, 'M9 9l6 6', 'M15 9l-6 6'],
}
// A computer's state as its status class, icon and words. The when column already
// shows a late computer's date, so its state does not repeat it.
const STATE = { ok: ['ok', 'ok', 'Backing up normally'], update: ['warn', 'clock', 'Needs update'], late: ['warn', 'clock', 'Not backed up recently'] }
// A health item's icon, row background, and what a screen reader hears before its text.
const LEVEL = { fail: ['fail', 'fail-row', 'Problem: '], warn: ['warn', 'warn-row', 'Warning: '], ok: ['ok', null, ''] }
const WOULD = 'would copy '
const TOTAL = /^would copy \d+ file\(s\), \d+ unchanged\.$/ // push's own count line, not a file
const LISTED = 500 // files the preview lists before "and N more"
const PREVIEW = 'cs.previewJob' // the last preview this tab started: its job reads like a backup's

const count = (n, word) => (n === 1 ? '1 ' + word : n.toLocaleString('en-GB') + ' ' + word + 's')
const lower = (s) => s.replace(/^Yesterday/, 'yesterday')
const plain = (e) => (e && e.plain ? e.message : GENERIC)
// at is a time as when() gives it. A zero time is unknown, never "not backing up"
// (Ruling P33): it shows "—" and is read as "Not known yet".
const at = (t, low) => {
  const s = when(t)
  if (s) return low ? lower(s) : s
  return h('span', null, h('span', { 'aria-hidden': 'true' }, '—'), h('span', { class: 'sr' }, 'Not known yet'))
}

// size is bytes in the CLI's binary units, as [number, decimals, unit]: 4.8 GB, 230 MB.
function size(b) {
  for (const [unit, n, d] of [['GB', 2 ** 30, 1], ['MB', 2 ** 20, 0], ['KB', 2 ** 10, 0]]) if (b >= n) return [b / n, d, unit]
  return [b, 0, 'B']
}
const sized = (b) => { const [v, d, u] = size(b); return v.toFixed(d) + ' ' + u }

register('overview', {
  // Only the summary holds the screen back; storage, health and activity fill in as
  // they come, so a slow health check holds up nothing else.
  async mount(el, params, signal) {
    const slot = (name) => el.querySelector(`[data-slot="${name}"]`)
    const pushBtn = slot('push'), previewBtn = slot('preview')
    let me = null // this computer's row: {c, cell, label, live}
    let progress = null // while a backup runs, its latest progress text ('' before any)
    let watching = false // one watcher at a time
    let raf = 0
    signal.addEventListener('abort', () => cancelAnimationFrame(raf))

    function showSummary(v) {
      slot('last').replaceChildren('Last backup ', at(v.lastBackup, true))
      if (v.problem) {
        el.querySelector('.split').replaceChildren(noArchive(v.problem, () => location.reload()))
        slot('usage').closest('section').hidden = true
        return
      }
      slot('provider').textContent = 'Archive in ' + (v.archive.provider || 'your sync folder') // Ruling P25
      slot('path').textContent = v.archive.display
      slot('counts').textContent = `${count(v.conversations, 'conversation')} and ${count(v.workFolders, 'work folder')}, from ${count(v.computers.length, 'computer')}`
      me = null
      const rows = v.computers.map(computer)
      slot('computers').replaceChildren(...(rows.length ? rows : [h('li', { class: 'muted' }, 'No computer has backed up to this archive yet.')]))
    }

    function computer(c) {
      const [cls, ic, words] = STATE[c.state] || STATE.update
      const sub = c.this ? 'This computer' + (c.version ? ', version ' + c.version : '') : c.version ? 'Version ' + c.version : 'An older version'
      const cell = h('span', { class: 'small num computer-when' })
      const label = whenCell(cell, c, c.this ? progress : null)
      // This computer's row says how its backup is going; the region is made once with
      // the row, so its updates are announced.
      const live = c.this ? h('span', { class: 'sr', role: 'status' }) : null
      if (c.this) me = { c, cell, label, live }
      return h('li', null,
        h('span', { class: 'grow computer-name' }, h('strong', null, c.name), h('span', { class: 'muted small' }, sub)),
        cell,
        h('span', { class: 'small num computer-count' }, h('span', { class: 'muted block' }, 'Conversations'), c.conversations.toLocaleString('en-GB')),
        h('span', { class: 'status computer-state ' + cls }, icon(16, ...ICON[ic]), words),
        live)
    }

    // whenCell shows the last backup, or, while a backup runs (text not null), its
    // progress text over the striped bar. It gives the label, which progress updates in
    // place: rebuilding the bar would restart its stripes.
    function whenCell(cell, c, text) {
      const label = h('span', { class: 'muted block' }, text == null ? 'Last backup' : text || 'Backing up')
      cell.replaceChildren(label,
        text == null ? at(c.lastPush) : h('span', { class: 'progress computer-progress', role: 'progressbar', 'aria-label': 'Backing up' }, h('span')))
      return label
    }
    const showRun = () => { if (me) me.label = whenCell(me.cell, me.c, progress) }
    const say = (text) => { if (me) me.live.textContent = text }
    const step = (text) => {
      progress = text
      if (me) me.label.textContent = text
      say(text)
    }

    // The size counts up over 900 ms, easing out, except under reduced motion.
    function countUp(node, bytes, animate) {
      const [v, d, u] = size(bytes)
      cancelAnimationFrame(raf)
      if (!animate || reducedMotion) { node.textContent = v.toFixed(d) + ' ' + u; return }
      const t0 = performance.now()
      const tick = (now) => {
        const k = Math.min(1, (now - t0) / 900)
        node.textContent = (v * (1 - (1 - k) ** 3)).toFixed(d) + ' ' + u
        if (k < 1) raf = requestAnimationFrame(tick)
      }
      raf = requestAnimationFrame(tick)
    }

    async function loadStorage(first) {
      let v
      try { v = await api.get('/api/storage') } catch (e) { if (!signal.aborted) slot('usage').textContent = plain(e); return }
      if (signal.aborted) return
      if (v.problem) { slot('usage').closest('section').hidden = true; return } // the archive card says why
      const { conversations, work, other } = v.sizes
      const total = conversations + work + other
      countUp(slot('size'), total, first)
      slot('usage').replaceChildren('Your archive uses ', h('strong', { class: 'num' }, sized(total)), v.archive.provider ? ' of ' + v.archive.provider + '.' : '.')
      const bar = slot('bar')
      for (const [part, b] of [['conversations', conversations], ['work', work], ['other', other]]) {
        bar.querySelector('.part-' + part).style.width = (total ? (100 * b) / total : 0) + '%'
        slot(part + '-size').textContent = sized(b)
      }
      bar.setAttribute('aria-label', `Conversations ${sized(conversations)}, work folders ${sized(work)}, pages and other files ${sized(other)}`)
      bar.hidden = slot('legend').hidden = false
      slot('saving').textContent = v.zipFrees > 0 ? `Compressing old conversations would free about ${sized(v.zipFrees)}.` : ''
    }

    // fix is a health item's link, or null: only a screen of this panel (#/...), and an
    // item h() refuses loses its link, never its row.
    function fix(i) {
      if (!i.fixLabel || typeof i.fixHref !== 'string' || !i.fixHref.startsWith('#/')) return null
      try { return h('a', { href: i.fixHref, class: 'btn sm' }, i.fixLabel) } catch { return null }
    }

    // Health shows the failures, the warnings, then that conversations are archived and
    // the scheduled backup is set up. It is doctor's light check (Ruling P54).
    async function loadHealth() {
      const list = slot('health')
      let items
      try { items = await api.get('/api/health') } catch (e) { if (!signal.aborted) list.replaceChildren(h('li', null, plain(e))); return }
      const shown = [...items.filter((i) => i.level === 'fail' || i.level === 'warn'),
        ...['archived', 'sweep'].flatMap((name) => items.filter((i) => i.level === 'ok' && i.name === name))]
      list.replaceChildren(...shown.map((i) => {
        const [ic, row, sr] = LEVEL[i.level]
        return h('li', { class: row }, h('span', { class: 'status ' + ic }, icon(16, ...ICON[ic])),
          h('span', { class: 'grow' }, sr && h('span', { class: 'sr' }, sr), i.text), fix(i))
      }))
      if (!shown.length) list.append(h('li', { class: 'muted' }, 'No problems found.'))
    }

    async function loadActivity(fresh) {
      const list = slot('activity')
      let items
      try { items = await api.get('/api/activity') } catch (e) { if (!signal.aborted) list.replaceChildren(h('li', null, plain(e))); return }
      list.replaceChildren(...items.map((a) => h('li', null, h('span', { class: 'muted activity-time' }, at(a.at)), h('span', { class: 'grow' }, a.text))))
      if (!items.length) list.append(h('li', { class: 'muted' }, 'No activity yet.'))
      else if (fresh) list.firstChild.classList.add('slide')
    }

    function refresh() {
      api.get('/api/summary').then((v) => { if (!signal.aborted) showSummary(v) }, () => { /* the page keeps what it shows */ })
      loadStorage(false)
      loadActivity(true)
    }

    // busy turns both buttons off while any job runs. Disabling the focused one dropped
    // focus to the page, so focus goes back to the button that was clicked.
    function busy(on, clicked) {
      pushBtn.disabled = previewBtn.disabled = on
      if (!on && clicked && document.activeElement === document.body) clicked.focus()
    }

    // start runs push, never quiet (Ruling P35); a 409 toasts which action is running.
    async function start(dryRun) {
      const btn = dryRun ? previewBtn : pushBtn
      busy(true) // before the request, so a double click starts one job
      let id
      try {
        ({ id } = await api.run('push', dryRun ? { dryRun: true } : {}))
      } catch (e) {
        busy(false, btn)
        if (!signal.aborted) toast(plain(e))
        return
      }
      if (dryRun) try { sessionStorage.setItem(PREVIEW, id) } catch { /* then a reload shows it as a backup until it ends */ }
      watch(id, dryRun ? 'preview' : 'backup', btn)
    }

    // watch follows a job to its end: a backup, a preview, or another screen's action,
    // which only keeps both buttons off. The end of a backup is announced by its toast.
    async function watch(id, kind, clicked) {
      if (watching) return
      watching = true
      busy(true)
      if (kind === 'backup') {
        pushBtn.textContent = 'Backing up'
        progress = ''
        showRun()
        say('Backing up')
      }
      let st
      try {
        st = await follow(id, { signal, onProgress: kind === 'backup' ? step : null })
      } catch (e) {
        st = { ok: false, error: plain(e) }
      }
      watching = false
      if (!st) return // the screen has gone; the job goes on, and the rail shows it
      pushBtn.textContent = 'Back up now'
      busy(false, clicked)
      if (kind === 'backup') {
        progress = null
        showRun()
        say('')
      }
      if (kind === 'other') return refresh()
      const res = st.result || {}
      if (st.ok && res.dryRun) return showPreview(res.copied || 0, st.lines || [])
      toast(!st.ok ? st.error || GENERIC : res.copied ? 'Backed up ' + count(res.copied, 'changed file') : 'Nothing new to back up')
      refresh()
    }

    function showPreview(n, lines) {
      const files = lines.filter((l) => l.startsWith(WOULD) && !TOTAL.test(l)).map((l) => l.slice(WOULD.length))
      const items = files.slice(0, LISTED).map((f) => h('li', null, f))
      if (files.length > LISTED) items.push(h('li', { class: 'muted' }, 'and ' + (files.length - LISTED).toLocaleString('en-GB') + ' more'))
      modal('Preview backup', null, (box, done) => {
        const id = box.getAttribute('aria-labelledby') + '-body'
        box.setAttribute('aria-describedby', id)
        const close = h('button', { type: 'button', class: 'btn', onclick: () => done(null) }, 'Close')
        box.append(h('p', { id }, n ? 'Would copy ' + count(n, 'file') : 'Nothing new to back up'))
        if (items.length) box.append(h('ul', { class: 'rows panel small mono dialog-items would-copy', tabindex: '0', 'aria-label': 'Files it would copy' }, items))
        box.append(h('div', { class: 'row end' }, close))
        return close
      })
    }

    pushBtn.addEventListener('click', () => start(false))
    previewBtn.addEventListener('click', () => start(true))
    const v = await api.get('/api/summary')
    showSummary(v)
    if (!v.problem) loadStorage(true)
    loadHealth()
    loadActivity(false)
    // A job already running (from another tab, another screen, or before a reload) keeps
    // both buttons off until it ends; a push shows here as a backup or a preview.
    api.get('/api/state').then((st) => {
      const job = st && st.job
      if (signal.aborted || !job || !job.running) return
      let mine = null
      try { mine = sessionStorage.getItem(PREVIEW) } catch { /* unknown: shown as a backup */ }
      watch(job.id, job.action !== 'push' ? 'other' : job.id === mine ? 'preview' : 'backup')
    }, () => { /* the rail foot shows any job */ })
  },
})
