// The Settings screen (Settings.dc.html): the archive folder, computers, work folders,
// where the other computers' folders go, automatic backup, rules and skills, readable
// pages, the full backup file, the log and About. Each control runs the CLI's command
// as a panel action, then reads the page again. Names, paths and log lines come from
// the archive or this computer: they reach the page only through h() and textContent.
import { h, icon, api, follow, toast, modal, confirmDialog, register, noArchive, when, allowExternal, README, GENERIC } from './app.js'
import { combo } from './combo.js'
import { chooseFolder } from './folders.js'

allowExternal(README) // the About link, the panel's only link to another site (Ruling P32)

const TOKEN = document.querySelector('meta[name="cs-token"]').content // for /api/stop, the one POST outside /api/run
const OK = ['M5 12.5l4.5 4.5L19 7.5']
const WAIT = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0-18 0', 'M12 7v5l3 2']
const EVERY = [[15, 'Every 15 minutes'], [30, 'Every 30 minutes'], [60, 'Every hour'], [120, 'Every 2 hours'], [-1, 'Only after replies']]
const AUTO_ON = 'On. After each reply in Claude Code, and on a schedule even when it is closed.'
const FORGET = 'Its conversations and work folders stay in the archive. It stops holding back compression and deleting. If it backs up again later, it comes back on its own.'
const SCAN = 'This reads every conversation on this computer in full, the ones brought in from your other computers too, and backs up each folder they name. Do it only if you trust everyone who can change your archive folder.'

const plain = (e) => (e && e.plain ? e.message : GENERIC)
const number = (n) => (n || 0).toLocaleString('en-GB')
const plural = (n, word) => number(n) + ' ' + word + (n === 1 ? '' : 's')
const same = (a, b) => String(a).toLowerCase() === String(b).toLowerCase() // computer names, compared as the CLI does
const slug = (p) => p.replace(/[^A-Za-z0-9]/g, '-') // Claude Code's folder name for a path (claude.Slug, under 200 characters)
const code = (t) => h('code', null, t)
const button = (label, onclick, attrs) => h('button', { type: 'button', class: 'btn sm', onclick, ...attrs }, label)

// size is "198 MB", "2.1 KB" or "7.9 GB".
function size(n) {
  for (const [u, s] of [[2 ** 30, 'GB'], [2 ** 20, 'MB'], [2 ** 10, 'KB']]) {
    if (n >= u) return (n / u < 10 ? (n / u).toFixed(1) : Math.round(n / u)) + ' ' + s
  }
  return plural(n, 'byte')
}

// day is "Today, 21:45", "Yesterday, 18:30" or "18 Nov"; "Unknown" for no time (Ruling P33).
function day(t) {
  const s = when(t)
  return !s ? 'Unknown' : /^\d\d:\d\d$/.test(s) ? 'Today, ' + s : s
}

// run waits for an action. A refusal (409) or a failure shows its plain sentence and
// gives null. btn is disabled meanwhile, so a double click starts one job.
async function run(btn, name, body) {
  if (btn) btn.disabled = true
  let st
  try {
    st = await api.run(name, body, { wait: true })
  } catch (e) {
    st = { ok: false, error: plain(e) }
  }
  if (btn) {
    btn.disabled = false
    if (document.activeElement === document.body && btn.isConnected) btn.focus() // disabling it dropped the focus
  }
  if (!st.ok) toast(st.error || GENERIC)
  return st.ok ? st : null
}
const config = (btn, action, ...args) => run(btn, 'config', { action, args })

// mapDialog asks which of the other computers' folders goes where on this computer, and
// gives {from, to} or null.
function mapDialog(title, label, options) {
  return modal(title, null, (box, done) => {
    let to = ''
    const add = h('button', { type: 'button', class: 'btn primary', disabled: true, onclick: () => done({ from: pick.value, to }) }, 'Add')
    const ready = () => { add.disabled = !(pick.value && to) }
    const field = h('div')
    const pick = combo(field, { label, options, value: '', onChange: ready })
    const target = h('p', { class: 'mono small map-target muted' }, 'No folder chosen yet')
    const choose = h('button', { type: 'button', class: 'btn', onclick: async () => {
      const p = await chooseFolder({ title: 'Choose where it goes on this computer' })
      if (!p) return
      to = target.textContent = p
      target.classList.remove('muted')
      ready()
    } }, 'Choose folder')
    box.append(field,
      h('div', { class: 'row fields' }, h('div', { class: 'grow stack gap-3' }, h('p', { class: 'lbl' }, 'Goes here on this computer'), target), choose),
      h('div', { class: 'row end' }, h('button', { type: 'button', class: 'btn', onclick: () => done(null) }, 'Cancel'), add))
    return pick.input
  })
}

register('settings', {
  async mount(el, params, signal) {
    const slot = (name) => el.querySelector(`[data-slot="${name}"]`)
    // Change folder is in the card that the no-archive state replaces: found before that.
    const card = slot('archive'), changeFolder = slot('change-folder'), nameInput = slot('name'), renameBtn = slot('rename'), pre = slot('log')
    const every = combo(slot('every'), { options: [], value: 30 })
    let v, cfg, me = '', home = '', sep = '/', log = null, diffs = [], projects = [], shownCard = card, healthN = 0
    const tilde = (p) => (home && (p === home || p.startsWith(home + sep)) ? '~' + p.slice(home.length) : p)

    async function load() {
      const [settings, lines, profile, proj, folders] = await Promise.all([
        api.get('/api/settings'),
        api.get('/api/log').catch(() => null),
        api.get('/api/profile').catch(() => null), // {problem} with no archive (Ruling P56)
        api.get('/api/projects').catch(() => null), // the archive's projects, for Add a project and Readable pages
        home ? null : api.get('/api/folders').catch(() => null), // the home folder, to show paths with ~
      ])
      // This computer's name, asked after /api/settings: the server holds the name a
      // config command loaded, and set-machine changes only the saved one.
      const state = await api.get('/api/state').catch(() => null)
      v = settings
      cfg = v.config || {}
      me = (state && state.machine) || ''
      log = Array.isArray(lines) ? lines : null
      diffs = Array.isArray(profile) ? profile : []
      projects = Array.isArray(proj) ? proj : []
      if (folders && folders.path) {
        home = folders.path
        sep = home.includes('\\') ? '\\' : '/'
      }
      paint()
    }

    // reload reads the page again after an action; focus goes to fallback when the
    // control that had it was redrawn.
    async function reload(fallback) {
      if (signal.aborted) return
      try {
        await load()
      } catch (e) {
        toast(plain(e))
      }
      if (fallback && !el.contains(document.activeElement)) fallback.focus()
    }

    // bucketAt is where a project folder in the archive was, on the computers that have it.
    function bucketAt(b) {
      for (const p of projects) {
        const at = (p.paths || []).filter((x) => slug(x.path) === b)
        if (at.length) return at
      }
      return []
    }

    function paint() {
      const next = v.problem ? noArchive(v.problem, () => reload()) : card
      if (next !== shownCard) {
        shownCard.replaceWith(next)
        shownCard = next
      }
      if (v.archive) card.querySelector('[data-slot="archive-label"]').replaceChildren(...(v.archive.provider ? [v.archive.provider + ', '] : []), code(v.archive.display), '.')
      // What needs an archive folder waits for one.
      for (const s of [slot('computers-box'), slot('computers-note'), slot('maps').closest('section'), slot('profile-line').closest('section'), slot('pages-line').closest('section')]) s.hidden = !!v.problem

      if (document.activeElement !== nameInput) nameInput.value = me // never over what is being typed
      renameBtn.disabled = !nameInput.value.trim() || nameInput.value.trim() === me
      const computers = v.computers || []
      slot('computers').replaceChildren(...(computers.length ? computers.map((c) => h('tr', null,
        h('td', null, h('strong', { class: 'medium' }, c.name), c.this && [' ', h('span', { class: 'muted' }, '(this computer)')]),
        h('td', null, c.version || 'Unknown'),
        h('td', null, day(c.lastPush)),
        h('td', null, c.ready ? h('span', { class: 'status ok' }, icon(14, ...OK), 'Ready') : h('span', { class: 'status warn' }, icon(14, ...WAIT), 'Needs update')),
        h('td', { class: 'r' }, !c.this && button('Forget', () => forget(c.name), { 'aria-label': 'Forget ' + c.name }))))
        : [h('tr', null, h('td', { colspan: 5, class: 'muted' }, 'No computer has backed up to this archive yet.'))]))

      slot('work').checked = !!cfg.workOn
      const folders = v.workFolders || []
      slot('folders').replaceChildren(...(folders.length ? folders.map((f) => h('tr', null,
        h('td', null, code(tilde(f.path))),
        h('td', { class: 'r' }, number(f.files)),
        h('td', { class: 'r' }, size(f.bytes || 0)),
        h('td', { class: 'r' }, button('Exclude', () => exclude(f.path), { 'aria-label': 'Exclude ' + tilde(f.path) }))))
        : [h('tr', null, h('td', { colspan: 4, class: 'muted' }, cfg.workOn ? 'No work folder is backed up from this computer yet.' : 'Work folders are not backed up.'))]))
      const excluded = cfg.workExclude || []
      slot('excluded').replaceChildren(...(excluded.length ? excluded.map((p) => h('li', null, h('code', { class: 'grow' }, tilde(p)),
        button('Include again', (e) => include(p, e.currentTarget), { 'aria-label': 'Include again: ' + tilde(p) })))
        : [h('li', { class: 'muted' }, 'No folder is excluded.')]))
      slot('placement-home').checked = cfg.placement !== 'machine'
      slot('placement-machine').checked = cfg.placement === 'machine'

      const maps = [
        ...Object.entries(cfg.workMap || {}).map(([from, to]) => ['Folder', from, to, 'unmap-folder']),
        ...Object.entries(cfg.projectMap || {}).map(([b, to]) => ['Project', (bucketAt(b)[0] || { path: b }).path, to, 'unmap', b]),
      ]
      slot('maps').replaceChildren(...(maps.length ? maps.map(([kind, from, to, action, arg]) => h('li', null,
        h('span', { class: 'grow' }, kind + ' ', code(from), ' goes to ', code(tilde(to))),
        button('Remove', (e) => unmap(action, arg || from, from, e.currentTarget), { 'aria-label': `Remove: ${kind} ${from}` })))
        : [h('li', { class: 'muted' }, 'None yet. Bring in finds most folders and projects on its own.')]))

      const n = cfg.sweepEvery || 30
      every.options = [...EVERY, ...(EVERY.some(([m]) => m === n) ? [] : [[n, `Every ${n} minutes`]])].map(([value, label]) => ({ value, label }))
      every.value = n

      const k = v.profileDiffs || 0
      slot('profile-line').textContent = k ? `${plural(k, 'file')} ${k === 1 ? 'differs' : 'differ'} between your computers. Each keeps its own until you choose.`
        : 'Your computers have the same rules and skills.'
      const newer = (d) => (!d.newest ? 'copies differ' : same(d.newest, me) ? 'newer here' : 'newer on ' + d.newest)
      slot('profile').replaceChildren(...diffs.slice(0, 3).map((d) => h('li', null, code(d.file), ', ', newer(d))),
        ...(diffs.length > 3 ? [h('li', null, `and ${number(diffs.length - 3)} more`)] : []))
      slot('profile').hidden = !diffs.length
      slot('compare').disabled = !diffs.length
      slot('take-newer').disabled = !k

      // Provider wording only when the provider is known (Ruling P25).
      const where = v.archive && v.archive.provider ? `in ${v.archive.provider}, on your phone too` : 'in your sync folder'
      const pg = v.pages || {}
      slot('pages-line').textContent = `Every conversation gets a page you can read ${where}. ` +
        (!pg.pages ? 'No pages yet.' : pg.stale ? `${plural(pg.pages, 'page')}, some behind the newest conversations.` : `${plural(pg.pages, 'page')}, all up to date.`)
      const left = cfg.renderExclude || []
      slot('left-out').replaceChildren(...(left.length ? left.flatMap((x, i) => [i ? ', ' : '', code(x)]) : ['none']))

      // Newest first, as the mockup shows it.
      pre.textContent = !log ? "The log can't be read right now." : log.length ? log.slice().reverse().join('\n') : 'Nothing in the log yet.'
    }

    function auto(kind, text) {
      const p = slot('auto-state')
      p.className = 'status ' + kind
      p.replaceChildren(icon(16, ...(kind === 'ok' ? OK : WAIT)), h('span', { class: 'status-text' }, text))
    }

    // Automatic backup's state is Health's hook and sweep checks (the Light check, Ruling P54).
    function paintAuto(items) {
      if (!items) return auto('warn', v.sweep.problem || "Automatic backup can't be checked right now.")
      const by = (name) => items.find((i) => i.name === name)
      const hook = by('session hook'), reply = by('reply hook'), sweep = by('sweep')
      const hooked = hook && hook.level === 'ok' && !reply
      if (hooked && sweep && sweep.level === 'ok') return auto('ok', AUTO_ON)
      if (hooked && cfg.sweepEvery === -1) return auto('ok', 'On. After each reply in Claude Code.')
      const off = [hook, reply, sweep].filter((i) => i && i.level !== 'ok')
      auto('warn', off.map((i) => i.text).join(' ') || "Automatic backup isn't set up.")
    }

    function loadHealth() {
      const n = ++healthN
      auto('info', 'Checking automatic backup…')
      const show = (items) => { if (n === healthN && !signal.aborted) paintAuto(items) }
      api.get('/api/health').then((items) => show(Array.isArray(items) ? items : null), () => show(null))
    }

    async function forget(name) {
      if (!await confirmDialog({ title: `Forget ${name}?`, body: FORGET, confirm: `Forget ${name}` })) return
      if (!await run(null, 'machines-forget', { name })) return
      toast(`Forgot ${name}. Its conversations stay in the archive.`)
      await reload(nameInput)
    }

    async function exclude(path) {
      const shown = tilde(path)
      if (!await confirmDialog({ title: `Exclude ${shown}?`, body: 'Its copy in the archive is deleted. The folder on this computer is not touched.', confirm: 'Exclude', danger: true })) return
      if (!await config(null, 'exclude-folder', path)) return
      toast(`Excluded ${shown}.`)
      await reload(slot('exclude'))
    }

    async function include(path, btn) {
      if (!await config(btn, 'unexclude-folder', path)) return
      toast(`Included ${tilde(path)} again.`)
      await reload(slot('exclude'))
    }

    async function unmap(action, arg, shown, btn) {
      if (!await config(btn, action, arg)) return
      toast(`Removed ${shown}. Bring in finds its place again.`)
      await reload(slot('add-folder'))
    }

    await load()

    renameBtn.addEventListener('click', rename)
    nameInput.addEventListener('input', () => { renameBtn.disabled = !nameInput.value.trim() || nameInput.value.trim() === me })
    nameInput.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !renameBtn.disabled) rename() })
    // Any name goes to the CLI, which decides; a refusal shows its sentence (Ruling P9).
    async function rename() {
      if (!await config(renameBtn, 'set-machine', nameInput.value.trim())) return
      toast('Renamed. The next backup uses the new name.')
      await reload(nameInput)
    }

    changeFolder.addEventListener('click', async (e) => {
      const btn = e.currentTarget
      const path = await chooseFolder({ title: 'Choose the archive folder' }) // the detected sync folders first (Ruling P52)
      if (!path || !await config(btn, 'set-destination', path)) return
      toast('Changed the archive folder.')
      await reload(btn)
    })

    const work = slot('work')
    work.addEventListener('change', async () => {
      const on = work.checked
      if (await config(work, 'work', on ? 'on' : 'off')) toast(on ? 'Backing up the folders Claude works in.' : 'Stopped backing up the folders Claude works in.')
      await reload(work)
    })
    for (const r of [slot('placement-home'), slot('placement-machine')]) {
      r.addEventListener('change', async () => {
        if (await config(r, 'work-placement', r.value)) {
          toast(r.value === 'machine' ? "Other computers' work folders go to exactly the same path here." : "Other computers' work folders go to the same place in your home folder.")
        }
        await reload(r)
      })
    }
    slot('exclude').addEventListener('click', async () => {
      const path = await chooseFolder({ title: 'Choose a folder to exclude' })
      if (path) exclude(path)
    })
    slot('scan-history').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      if (!await confirmDialog({ title: 'Look through older conversations?', body: SCAN, confirm: 'Look through older conversations' })) return
      const st = await run(btn, 'push', { scanHistory: true })
      if (!st) return
      toast(`Looked through older conversations and backed up ${plural((st.result && st.result.copied) || 0, 'changed file')}.`)
      await reload(btn)
    })

    // The other computers' work folders are in their records, which a dry run of import reads.
    slot('add-folder').addEventListener('click', async (e) => {
      const btn = e.currentTarget, status = slot('map-status')
      status.textContent = 'Looking through the archive…'
      const st = await run(btn, 'import', { dryRun: true })
      status.textContent = ''
      if (!st || signal.aborted) return
      const seen = new Set()
      const options = (Array.isArray(st.result) ? st.result : [])
        .filter((r) => r.kind === 'work' && r.recordedPath && !seen.has(r.recordedPath) && seen.add(r.recordedPath))
        .map((r) => ({ value: r.recordedPath, label: r.machine ? `${r.recordedPath} (${r.machine})` : r.recordedPath }))
      if (!options.length) return toast('Your other computers have no work folders in the archive.')
      const pick = await mapDialog('Add a folder', 'A work folder on another computer', options)
      if (!pick || !await config(btn, 'map-folder', pick.from, pick.to)) return
      toast(`Added. ${pick.from} goes to ${tilde(pick.to)}.`)
      await reload(btn)
    })
    slot('add-project').addEventListener('click', async (e) => {
      const btn = e.currentTarget, options = []
      for (const p of projects) {
        for (const b of p.buckets || []) {
          const at = bucketAt(b)
          if (at.length && at.every((x) => same(x.machine, me))) continue // this computer's own
          options.push({ value: b, label: at.length ? `${at[0].path} (${at.map((x) => x.machine).join(', ')})` : `${p.name} (${b})` })
        }
      }
      if (!options.length) return toast('Your other computers have no projects in the archive.')
      const pick = await mapDialog('Add a project', 'A project from another computer', options)
      if (!pick || !await config(btn, 'map', pick.from, pick.to)) return
      toast(`Added. ${(bucketAt(pick.from)[0] || { path: pick.from }).path} goes to ${tilde(pick.to)}.`)
      await reload(btn)
    })

    // Only after replies is install --no-sweep, which also removes the sweep (Rulings P40, P41).
    slot('install').addEventListener('click', async (e) => {
      const btn = e.currentTarget, n = every.value
      const st = await run(btn, 'install', n === -1 ? { noSweep: true } : { every: n })
      if (!st) return
      toast(st.result && st.result.sweepFailed ? "Set up again, but the scheduled backup didn't change. The details are in the log."
        : 'Set up automatic backup again. Restart Claude Code to use it.')
      await reload(btn)
      loadHealth()
    })
    slot('uninstall').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      if (!await confirmDialog({ title: 'Turn off automatic backup?', body: 'Your archive is kept.', confirm: 'Turn off automatic backup', danger: true })) return
      if (!await run(btn, 'uninstall', {})) return
      toast('Turned off automatic backup. Your archive is kept.')
      await reload(btn)
      loadHealth()
    })

    slot('compare').addEventListener('click', () => modal('Compare rules and skills', null, (box, done) => {
      const close = h('button', { type: 'button', class: 'btn', onclick: () => done(null) }, 'Close')
      box.append(h('p', { class: 'muted' }, "Each file whose copies differ, with every computer's copy. Nothing changes until you take the newer copies."),
        h('ul', { class: 'rows panel small compare-list', tabindex: '0', 'aria-label': 'Files that differ' }, diffs.map((d) => h('li', null, code(d.file),
          h('ul', { class: 'compare-copies' }, (d.copies || []).map((c) => h('li', null, h('span', { class: 'medium' }, c.machine), `${size(c.size)}, ${day(c.modTime)}`,
            same(c.machine, d.newest) && h('span', { class: 'badge green' }, 'Newest'))))))),
        h('div', { class: 'row end' }, close))
      return close
    }))
    slot('take-newer').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      if (!await confirmDialog({ title: 'Take the newer copies here?', confirm: 'Take the newer copies here',
        body: "This computer's CLAUDE.md, rules and skills are replaced by the newer copies from your other computers. This computer's own copies stay in the archive." })) return
      const st = await run(btn, 'import', { profileOnly: true })
      if (!st) return
      const files = (st.result && st.result.files) || []
      toast(files.length ? `Took the newer copies of ${plural(files.length, 'file')} here. This computer's own copies stay in the archive.`
        : 'Nothing newer to take: this computer has the newest copies.')
      await reload(btn)
    })

    slot('change-left-out').addEventListener('click', async (e) => {
      const btn = e.currentTarget, left = cfg.renderExclude || []
      const names = [...new Set(projects.map((p) => p.name).filter((n) => n && !left.includes(n)))]
      const pick = await modal('Readable pages', null, (box, done) => {
        const field = h('div')
        const leave = h('button', { type: 'button', class: 'btn', disabled: true, onclick: () => done({ exclude: c.value }) }, 'Leave out')
        const c = combo(field, { label: 'Leave a project out of pages', options: names.map((n) => ({ value: n, label: n })), value: '', onChange: () => { leave.disabled = !c.value } })
        const close = h('button', { type: 'button', class: 'btn', onclick: () => done(null) }, 'Close')
        box.append(h('h3', null, 'Left out of pages'),
          left.length ? h('ul', { class: 'rows ruled' }, left.map((x) => h('li', null, h('code', { class: 'grow' }, x),
            button('Publish again', () => done({ include: x }), { 'aria-label': 'Publish again: ' + x }))))
            : h('p', { class: 'muted' }, 'No project is left out of pages.'),
          h('div', { class: 'row fields' }, h('div', { class: 'grow' }, field), leave),
          h('div', { class: 'row end' }, close))
        return close
      })
      if (!pick) return
      if (!await run(btn, 'render', pick.include ? { include: [pick.include] } : { exclude: [pick.exclude] })) return
      toast(pick.include ? `Published pages for ${pick.include} again.` : `Left ${pick.exclude} out of pages.`)
      await reload(btn)
    })
    slot('rebuild').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      if (!await run(btn, 'render', { force: true })) return
      toast('Rebuilt all pages.')
      await reload(btn)
    })

    slot('backup-preview').addEventListener('click', async (e) => {
      const btn = e.currentTarget, out = slot('backup-result')
      out.textContent = 'Counting…'
      const st = await run(btn, 'backup', { dryRun: true })
      const r = st && st.result
      out.textContent = r ? `${plural(r.files, 'file')}, ${plural(r.transcripts, 'conversation')}` : ''
    })
    slot('backup').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      const list = home ? await api.get('/api/folders').catch(() => null) : null
      const start = list && (list.entries || []).some((f) => f.name === 'claude-backups') ? home + sep + 'claude-backups' : home
      const dir = await chooseFolder({ title: 'Choose where to save the backup file', start })
      if (!dir) return
      const st = await run(btn, 'backup', { out: dir })
      if (!st) return
      toast(`Created backup file ${((st.result && st.result.target) || '').split(/[\\/]/).pop()}.`)
      slot('backup-result').textContent = ''
    })

    slot('open-log').addEventListener('click', (e) => run(e.currentTarget, 'open-log', {}))
    // The full check is doctor with every check, as a job (Ruling P54); its report shows
    // where the log was until the page is read again.
    slot('full-check').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      btn.disabled = true
      let st = null
      try {
        const { id } = await api.run('doctor', {})
        pre.textContent = ''
        st = await follow(id, { signal, onLine: (s) => pre.append(s + '\n') })
      } catch (err) {
        toast(plain(err))
      }
      btn.disabled = false
      if (document.activeElement === document.body) btn.focus()
      if (!st) return
      if (!st.ok) return toast(st.error || GENERIC)
      const r = st.result || {}
      toast(r.failures || r.warnings ? `Full check done: ${plural(r.failures, 'problem')} and ${plural(r.warnings, 'warning')}. Its report is in the log box.`
        : 'Full check done: everything is in order.')
    })

    slot('about').replaceChildren(`claude-sessions ${v.version}. `, h('a', { href: README, target: '_blank', rel: 'noopener noreferrer' }, 'Read the guide'), '. ',
      h('a', { href: '#/commands' }, 'Every command and where it is in the panel'), '.')
    // Stop answers "now", or "after" the running job (Ruling P12).
    const stop = slot('stop'), note = slot('stop-note')
    note.textContent = v.fromShortcut ? 'It stops by itself 10 minutes after you close this tab.' : 'Closing this tab leaves it running.'
    stop.addEventListener('click', async () => {
      stop.disabled = true
      let r = null
      try {
        const res = await fetch('/api/stop', { method: 'POST', credentials: 'same-origin', headers: { 'X-CS-Token': TOKEN } })
        if (res.ok) r = await res.json()
      } catch { /* told below */ }
      if (r && r.stopping === 'after') {
        note.textContent = `Stopping after ${r.label} finishes.`
      } else if (r && r.stopping === 'now') {
        note.textContent = 'Stopping now.'
        // The first request that finds no panel shows the stopped notice.
        const gone = () => api.get('/api/state').then(() => { if (!signal.aborted) setTimeout(gone, 1000) }, () => {})
        setTimeout(gone, 300)
      } else {
        stop.disabled = false
        toast(GENERIC)
      }
    })

    loadHealth()
  },
})
