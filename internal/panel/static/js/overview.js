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
// What a screen reader hears before a health item's text.
const LEVEL = { fail: 'Problem: ', warn: 'Warning: ', ok: '' }
const CHEVRON = 'M6 9l6 6 6-6'
// Recent activity's icons. The API gives each event only as words, so its kind is read
// from them (activityText and logActivity in the CLI write them); the first match wins.
const KINDS = [
  [/ backed up$/, 'computer'], // another computer's last backup
  [/^(Backed up|Made a full backup)/, 'backup'],
  [/^Compressed/, 'compress'],
  [/^(Brought in|Restored)/, 'bring'],
  [/^A backup did not finish/, 'problem'],
  [/^(Changed|Renamed|Added to|Removed from|Stopped|Backing up a folder|Set up|Turned off|Forgot)/, 'setting'],
  [/^(Deleted|Removed)/, 'clean'],
]
const TRAY = 'M4 15v4a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-4'
const KIND = { // [tile tint, icon]
  backup: ['', ['M12 15V4', 'M7.5 8.5L12 4l4.5 4.5', TRAY]],
  computer: ['', ['M4 4h16a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z', 'M8 20h8', 'M12 16v4']],
  bring: ['plain', ['M12 4v11', 'M7.5 10.5L12 15l4.5-4.5', TRAY]],
  compress: ['plain', ['M12 3v6', 'M9 6l3 3 3-3', 'M12 21v-6', 'M9 18l3-3 3 3', 'M5 12h14']],
  clean: ['plain', ['M4 7h16', 'M9 7V4.5h6V7', 'M6.5 7l.9 12.1a1 1 0 0 0 1 .9h7.2a1 1 0 0 0 1-.9L17.5 7', 'M10 11v5', 'M14 11v5']],
  setting: ['plain', ['M4 6h9', 'M17 6h3', 'M4 12h3', 'M11 12h9', 'M4 18h11', 'M13 6a2 2 0 1 0 4 0a2 2 0 1 0-4 0', 'M7 12a2 2 0 1 0 4 0a2 2 0 1 0-4 0', 'M15 18a2 2 0 1 0 4 0a2 2 0 1 0-4 0']],
  problem: ['warn', ICON.warn],
  other: ['plain', ['M9 12a3 3 0 1 0 6 0a3 3 0 1 0-6 0']],
}
const kindOf = (text) => (KINDS.find(([re]) => re.test(text)) || [null, 'other'])[1]
const WOULD = 'would copy '
const TOTAL = /^would copy \d+ file\(s\), \d+ unchanged\.$/ // push's own count line, not a file
const LISTED = 50 // files the preview lists before "and N more"
// STEP is push's progress text: "<stage>: n/m" or "<stage>", e.g. "copying conversations: 120/2000".
const STEP = /^(.*?)(?::\s*(\d+)\s*\/\s*(\d+))?\s*$/
const PREVIEW = 'cs.previewJob' // the last preview this tab started: its job reads like a backup's

const count = (n, word) => (n === 1 ? '1 ' + word : n.toLocaleString('en-GB') + ' ' + word + 's')
const plain = (e) => (e && e.plain ? e.message : GENERIC)
// about is a time kept only to the hour, as another computer's last backup is (push
// keeps the hour): "About 07:00", "Yesterday, about 18:00", or the day.
function about(t) {
  const s = when(t)
  return s && (/^\d\d:\d\d$/.test(s) ? 'About ' + s : s.replace(/^Yesterday, /, 'Yesterday, about '))
}
// at is a time as when() gives it, or as about() does when rough. A zero time is
// unknown, never "not backing up" (Ruling P33): it shows "—" and is read as "Not known yet".
const at = (t, rough) => {
  const s = rough ? about(t) : when(t)
  if (s) return s
  return h('span', null, h('span', { 'aria-hidden': 'true' }, '—'), h('span', { class: 'sr' }, 'Not known yet'))
}

// size is bytes in the CLI's binary units, as [number, decimals, unit]: 4.8 GB, 230 MB.
function size(b) {
  for (const [unit, n, d] of [['GB', 2 ** 30, 1], ['MB', 2 ** 20, 0], ['KB', 2 ** 10, 0]]) if (b >= n) return [b / n, d, unit]
  return [b, 0, 'B']
}
const sized = (b) => { const [v, d, u] = size(b); return v.toFixed(d) + ' ' + u }
const exact = (t) => new Date(t).toLocaleString('en-GB', { dateStyle: 'medium', timeStyle: 'short' })

// ago is "Just now", "12 min ago" or "3 h ago" up to 12 hours, then when()'s words; null
// for no time.
function ago(t) {
  const s = when(t)
  if (!s) return null
  const m = Math.floor((Date.now() - new Date(t)) / 60000)
  return m < 1 ? 'Just now' : m < 60 ? m + ' min ago' : m < 720 ? Math.floor(m / 60) + ' h ago' : s
}

// stamp shows time t in node as ago() does, the exact time in its title, or, rough, as
// about() does (an hour has no exact minute to give). It marks node for the minute's
// tick, which calls it again; with no time it shows at()'s "Not known yet".
function stamp(node, t, rough) {
  const s = rough ? about(t) : ago(t)
  if (!s) {
    node.replaceChildren(at(t))
    node.removeAttribute('data-at')
    node.removeAttribute('title')
    return node
  }
  node.textContent = s
  node.dataset.at = t
  node.toggleAttribute('data-rough', !!rough)
  if (rough) node.removeAttribute('title')
  else node.title = exact(t)
  return node
}

register('overview', {
  // Only the summary holds the screen back; storage, health and activity fill in as
  // they come, so a slow health check holds up nothing else.
  async mount(el, params, signal) {
    const slot = (name) => el.querySelector(`[data-slot="${name}"]`)
    const pushBtn = slot('push'), previewBtn = slot('preview')
    let me = null // this computer's row: {c, cell, label, live}
    let progress = null // while a backup runs, its latest progress text ('' before any)
    let firstRun = null // while this computer's first backup runs: its row {row, label}
    let watching = false // one watcher at a time
    const frames = new Map() // each counting figure's next frame
    // Relative times ("5 min ago") move on once a minute while the screen is open.
    const ticker = setInterval(() => { for (const n of el.querySelectorAll('[data-at]')) stamp(n, n.dataset.at, n.hasAttribute('data-rough')) }, 60000)
    signal.addEventListener('abort', () => { for (const f of frames.values()) cancelAnimationFrame(f); clearInterval(ticker) })

    // showSummary fills the archive card and the computers; first, the figures count up.
    function showSummary(v, first) {
      if (v.problem) {
        el.querySelector('.split').replaceChildren(noArchive(v.problem, () => location.reload()))
        slot('usage').closest('section').hidden = true
        return
      }
      slot('provider').textContent = 'Archive in ' + (v.archive.provider || 'your sync folder') // Ruling P25
      slot('path').textContent = v.archive.display
      const whole = (n) => Math.round(n).toLocaleString('en-GB')
      countUp(slot('n-conversations'), v.conversations, whole, first)
      countUp(slot('n-work'), v.workFolders, whole, first)
      countUp(slot('n-computers'), v.computers.length, whole, first)
      stamp(slot('n-last'), v.lastBackup)
      me = firstRun = null
      const rows = v.computers.map(computer)
      slot('computers').replaceChildren(...(rows.length ? rows : [h('li', { class: 'muted none-yet' }, 'No computer has backed up to this archive yet.')]))
      if (progress != null) showRun()
    }

    function computer(c) {
      const [cls, ic, words] = STATE[c.state] || STATE.update
      const sub = c.this ? 'This computer' + (c.version ? ', version ' + c.version : '') : c.version ? 'Version ' + c.version : 'An older version'
      const cell = h('span', { class: 'small num computer-when' })
      const label = whenCell(cell, c, c.this ? progress : null)
      // The dot is the state at a glance; this computer's pulses while it backs up.
      const dot = h('span', { class: 'dot' + (cls === 'ok' ? '' : ' warn') + (c.this && progress != null ? ' live' : ''), 'aria-hidden': 'true' })
      if (c.this) me = { c, cell, label, dot }
      return h('li', null,
        h('span', { class: 'grow computer-name' }, h('strong', null, dot, c.name), h('span', { class: 'muted small' }, sub)),
        cell,
        h('span', { class: 'small num computer-count' }, h('span', { class: 'muted block' }, 'Conversations'), c.conversations.toLocaleString('en-GB')),
        h('span', { class: 'status computer-state ' + cls }, icon(16, ...ICON[ic]), words))
    }

    // whenCell shows the last backup, or, while a backup runs (text not null), its
    // progress text over the striped bar. It gives the label, which progress updates in
    // place: rebuilding the bar would restart its stripes.
    function whenCell(cell, c, text) {
      const label = h('span', { class: 'muted block' }, text == null ? 'Last backup' : text || 'Backing up')
      cell.replaceChildren(label,
        text == null ? at(c.lastPush, !c.this) : h('span', { class: 'progress computer-progress', role: 'progressbar', 'aria-label': 'Backing up' }, h('span')))
      return label
    }
    // showRun shows a backup running (progress not null) in this computer's row. Before
    // its first backup this computer has no row: the card gets one that says so.
    function showRun() {
      if (me) {
        me.label = whenCell(me.cell, me.c, progress)
        me.dot.classList.toggle('live', progress != null)
        return
      }
      const list = slot('computers'), none = list.querySelector('.none-yet')
      if (none) none.hidden = progress != null
      if (progress == null) { if (firstRun) firstRun.row.remove(); firstRun = null; return }
      if (firstRun) { firstRun.label.textContent = progress || 'Backing up'; return }
      const label = h('span', { class: 'muted block' }, progress || 'Backing up')
      firstRun = { label, row: h('li', null,
        h('span', { class: 'grow computer-name' }, h('strong', null, h('span', { class: 'dot live', 'aria-hidden': 'true' }), 'This computer'),
          h('span', { class: 'muted small' }, 'Its first backup is running')),
        h('span', { class: 'small num computer-when' }, label, h('span', { class: 'progress computer-progress', 'aria-hidden': 'true' }, h('span')))) }
      list.prepend(firstRun.row)
    }
    // say speaks in the screen's one live region: the start, each new stage, the end.
    const say = (text) => { slot('say').textContent = text }

    // step shows a job's progress text above the cards: its stage, and a bar that fills
    // when the text has a count; until then, doing. It gives the text for the row.
    let stage = ''
    function step(text, doing) {
      const [, s, n, m] = STEP.exec(text || '') || []
      const words = s ? s[0].toUpperCase() + s.slice(1) : doing
      const of = Number(m || 0), pct = of ? Math.min(100, Math.round((Number(n) / of) * 100)) : null
      const counted = of ? `${Number(n).toLocaleString('en-GB')} of ${of.toLocaleString('en-GB')}` : ''
      slot('run-stage').textContent = words
      slot('run-count').textContent = counted
      const bar = slot('run-bar')
      if (pct == null) bar.removeAttribute('aria-valuenow')
      else { bar.style.setProperty('--p', pct / 100); bar.setAttribute('aria-valuenow', pct) } // panel.css fills it by transform
      if (words !== stage) { stage = words; say(words) } // a stage, not every count
      return counted ? words + ': ' + counted : words
    }

    // countUp shows show(to) in node; animate, it counts up to it over 600 ms, easing
    // out, except under reduced motion.
    function countUp(node, to, show, animate) {
      cancelAnimationFrame(frames.get(node))
      if (!animate || reducedMotion) { node.textContent = show(to); return }
      const t0 = performance.now()
      const tick = (now) => {
        const k = Math.min(1, (now - t0) / 600)
        node.textContent = show(to * (1 - (1 - k) ** 3))
        if (k < 1) frames.set(node, requestAnimationFrame(tick))
      }
      frames.set(node, requestAnimationFrame(tick))
    }

    async function loadStorage(first) {
      let v
      try { v = await api.get('/api/storage') } catch (e) { if (!signal.aborted) slot('usage').textContent = plain(e); return }
      if (signal.aborted) return
      if (v.problem) { slot('usage').closest('section').hidden = true; return } // the archive card says why
      const { conversations, work, other } = v.sizes
      const total = conversations + work + other
      const [sv, sd, su] = size(total) // counts in its final unit, so the unit never changes
      countUp(slot('size'), sv, (x) => x.toFixed(sd) + ' ' + su, first)
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

    // Health says how things stand in one line: "Everything is fine", or how many things
    // to look at, with the worst one's icon and colour. Its items open under it: the
    // failures, the warnings, then that conversations are archived and the scheduled
    // backup is set up. It is doctor's light check (Ruling P54). It opens by itself when
    // a Problem appears, and otherwise stays as it was left.
    let healthOpen = null, hadFail = false
    async function loadHealth() {
      const box = slot('health')
      let items
      try { items = await api.get('/api/health') } catch (e) { box.removeAttribute('aria-busy'); if (!signal.aborted) box.replaceChildren(h('p', { class: 'health-sum' }, plain(e))); return }
      if (signal.aborted) return
      box.removeAttribute('aria-busy')
      const bad = items.filter((i) => i.level === 'fail' || i.level === 'warn')
      const shown = [...bad, ...['archived', 'sweep'].flatMap((name) => items.filter((i) => i.level === 'ok' && i.name === name))]
      const fail = bad.some((i) => i.level === 'fail'), worst = fail ? 'fail' : bad.length ? 'warn' : 'ok'
      if (healthOpen == null || (fail && !hadFail)) healthOpen = fail
      hadFail = fail
      const list = h('ul', { class: 'rows health-items' }, shown.map((i) =>
        h('li', null, h('span', { class: 'status ' + i.level }, icon(16, ...ICON[i.level])),
          h('span', null, LEVEL[i.level] && h('span', { class: 'sr' }, LEVEL[i.level]), i.text), fix(i))))
      if (!shown.length) list.append(h('li', { class: 'muted' }, 'No problems found.'))
      const id = 'health-items'
      const sum = h('button', { type: 'button', class: 'health-sum', 'aria-expanded': String(healthOpen), 'aria-controls': id, onclick: (e) => {
        healthOpen = !healthOpen
        box.classList.toggle('instant', e.detail === 0) // opened from the keyboard: at once
        sum.setAttribute('aria-expanded', String(healthOpen))
      } },
      h('span', { class: 'tile ' + worst }, icon(16, ...ICON[worst])),
      h('span', { class: 'grow' }, bad.length ? count(bad.length, 'thing') + ' to look at' : 'Everything is fine'),
      icon(16, CHEVRON))
      sum.lastChild.classList.add('chev')
      const refocus = box.contains(document.activeElement)
      box.replaceChildren(sum, h('div', { class: 'health-body', id }, h('div', null, list)))
      if (refocus) sum.focus()
    }

    async function loadActivity(fresh) {
      const list = slot('activity')
      let items
      try { items = await api.get('/api/activity') } catch (e) { list.removeAttribute('aria-busy'); if (!signal.aborted) list.replaceChildren(h('li', null, plain(e))); return }
      list.removeAttribute('aria-busy')
      list.replaceChildren(...items.map((a) => {
        const kind = kindOf(a.text), [tint, paths] = KIND[kind]
        // "<computer> backed up" is another computer's last backup, kept only to the hour.
        return h('li', { class: 'act' }, h('span', { class: 'tile ' + tint }, icon(15, ...paths)), h('span', { class: 'act-text' }, a.text),
          stamp(h('time', { class: 'act-time num', datetime: a.at }), a.at, kind === 'computer'))
      }))
      if (!items.length) list.append(h('li', { class: 'muted' }, 'No activity yet.'))
      else if (fresh) list.firstChild.classList.add('slide')
    }

    // After a job: the server dropped its kept health answer then (Ruling P56), so the
    // backup that fixes a Problem row also clears it.
    function refresh() {
      api.get('/api/summary').then((v) => { if (!signal.aborted) showSummary(v) }, () => { /* the page keeps what it shows */ })
      loadStorage(false)
      loadHealth()
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
      const mine = kind !== 'other', doing = kind === 'backup' ? 'Backing up' : 'Checking what a backup would copy'
      if (mine) {
        stage = ''
        step('', doing)
        slot('run').hidden = false
      }
      if (kind === 'backup') {
        pushBtn.textContent = 'Backing up'
        progress = ''
        showRun()
      }
      const onProgress = (text) => {
        const p = step(text, doing)
        if (kind !== 'backup') return
        progress = p
        if (me) me.label.textContent = p
        else showRun()
      }
      let st
      try {
        st = await follow(id, { signal, onProgress: mine ? onProgress : null })
      } catch (e) {
        st = { ok: false, error: plain(e) }
      }
      watching = false
      if (!st) return // the screen has gone; the job goes on, and the rail shows it
      pushBtn.textContent = 'Back up now'
      busy(false, clicked)
      slot('run').hidden = true
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
    showSummary(v, true)
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
