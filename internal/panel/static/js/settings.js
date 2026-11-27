// The Settings screen (Settings.dc.html): the archive folder, computers, work folders,
// where the other computers' folders go, automatic backup, rules and skills, readable
// pages, the full backup file, the log and About. Each control runs the CLI's command
// as a panel action, then reads the page again. Names, paths and log lines come from
// the archive or this computer: they reach the page only through h() and textContent.
import { h, icon, api, follow, toast, modal, confirmDialog, register, noArchive, chooseArchive, ownFolder, when, allowExternal, README, GENERIC } from './app.js'
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
const TURN_OFF = 'Your archive is kept. The panel shortcut is removed too; run Set up again to bring both back.' // Ruling P64

const plain = (e) => (e && e.plain ? e.message : GENERIC)
const number = (n) => (n || 0).toLocaleString('en-GB')
const plural = (n, word) => number(n) + ' ' + word + (n === 1 ? '' : 's')
const same = (a, b) => String(a).toLowerCase() === String(b).toLowerCase() // computer names, compared as the CLI does
const slug = (p) => p.replace(/[^A-Za-z0-9]/g, '-') // Claude Code's folder name for a path (claude.Slug, under 200 characters)
const code = (t) => h('code', null, t)
const button = (label, onclick, attrs) => h('button', { type: 'button', class: 'btn sm', onclick, ...attrs }, label)
const busy = (el) => el.getAttribute('aria-disabled') === 'true'

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

// about is another computer's last backup, which its record keeps only to the hour (UTC):
// "Today, about 07:00", "Yesterday, about 18:00" or "18 Nov".
function about(t) {
  const s = when(t)
  return !s ? 'Unknown' : /^\d\d:\d\d$/.test(s) ? 'Today, about ' + s : s.replace(/^Yesterday, /, 'Yesterday, about ')
}

// leftOutBy is every project that render --exclude <pattern> leaves out, by
// render.IsExcluded's rule: the pattern, trimmed and in any case, inside a project's path
// or folder name. It is applied on every computer, so it is checked against them all.
function leftOutBy(projects, pattern) {
  const q = pattern.trim().toLowerCase()
  return q ? projects.filter((p) => [...(p.buckets || []), ...(p.paths || []).map((x) => x.path)].some((s) => s.toLowerCase().includes(q))) : []
}

// run waits for an action. While it runs, btn is aria-disabled, which keeps the focus on
// it (the screen's click guard ignores it), and the section's status line says doing. A
// refusal (409) or a failure shows its plain sentence, in error when given, and gives null.
async function run(btn, name, body, doing, { error } = {}) {
  const section = btn.closest('section, .dialog')
  const status = section && section.querySelector('.job-status')
  btn.setAttribute('aria-disabled', 'true')
  if (status) status.textContent = doing
  if (error) error.hidden = true
  let st
  try {
    st = await api.run(name, body, { wait: true })
  } catch (e) {
    st = { ok: false, error: plain(e) }
  }
  btn.removeAttribute('aria-disabled')
  if (status) status.textContent = ''
  if (st.ok) return st
  if (error) {
    error.textContent = st.error || GENERIC
    error.hidden = false
  } else toast(st.error || GENERIC)
  return null
}
const config = (btn, doing, action, ...args) => run(btn, 'config', { action, args }, doing)

// mapDialog asks which of the other computers' folders goes where on this computer, and
// runs add(from, to, button, error) from its Add button. A refusal shows in the dialog,
// which keeps what was chosen; it gives the {from, to} that was added, or null.
function mapDialog(title, label, options, add) {
  return modal(title, null, (box, done) => {
    let to = ''
    const error = h('p', { class: 'note red', role: 'alert', hidden: true })
    const addBtn = h('button', { type: 'button', class: 'btn primary', disabled: true, onclick: async () => {
      const from = pick.value
      if (await add(from, to, addBtn, error)) done({ from, to })
    } }, 'Add')
    const ready = () => { addBtn.disabled = !(pick.value && to) }
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
      error,
      h('div', { class: 'row end' }, h('span', { class: 'muted small job-status', role: 'status' }),
        h('button', { type: 'button', class: 'btn', onclick: () => done(null) }, 'Cancel'), addBtn))
    return pick.input
  })
}

register('settings', {
  async mount(el, params, signal) {
    const slot = (name) => el.querySelector(`[data-slot="${name}"]`)
    // Change folder is in the card that the no-archive state replaces: found before that.
    const card = slot('archive'), changeFolder = slot('change-folder'), nameInput = slot('name'), renameBtn = slot('rename'), pre = slot('log')
    const everyHint = slot('every-hint')
    let pendingEvery = null // a Scheduled backup choice not yet set up, kept across reloads
    const every = combo(slot('every'), { options: [], value: 30, onChange: (n) => { pendingEvery = n; hintEvery() } })
    let v, cfg, me = '', home = '', sep = '/', log = null, diffs = [], profileState = 'comparing', projects = [], shownCard = card, healthN = 0
    const tilde = (p) => (home && (p === home || p.startsWith(home + sep)) ? '~' + p.slice(home.length) : p)

    // A control running its job is aria-disabled, not disabled, so it keeps the focus;
    // this guard drops its clicks, Enter and Space included (Space on a switch too).
    document.addEventListener('click', (e) => {
      if (e.target.closest && e.target.closest('[aria-disabled="true"]')) {
        e.preventDefault()
        e.stopImmediatePropagation()
      }
    }, { capture: true, signal })

    async function load() {
      // Rules and skills come on their own: comparing every computer's copies can take
      // minutes on a folder that downloads files as they are read, and the rest of the
      // page does not wait for it.
      const profile = api.get('/api/profile').catch(() => null) // {problem} with no archive (Ruling P56)
      const [settings, lines, proj, folders] = await Promise.all([
        api.get('/api/settings'),
        api.get('/api/log').catch(() => null),
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
      profileState = 'comparing'
      diffs = []
      projects = Array.isArray(proj) ? proj : []
      if (folders && folders.path) {
        home = folders.path
        sep = home.includes('\\') ? '\\' : '/'
      }
      paint()
      profile.then((p) => {
        if (signal.aborted || v !== settings) return // left, or a newer load has painted
        profileState = Array.isArray(p) ? 'read' : 'failed'
        diffs = Array.isArray(p) ? p : []
        paintProfile()
      })
    }

    // reload reads the page again after an action. Focus then goes to focus (an element,
    // or a function giving one) when the control that had it was redrawn.
    async function reload(focus) {
      if (signal.aborted) return
      try {
        await load()
      } catch (e) {
        toast(plain(e))
      }
      const to = typeof focus === 'function' ? focus() : focus
      if (to && !el.contains(document.activeElement)) to.focus()
    }

    // heading makes a section's heading the place focus goes when its last control is gone.
    function heading(id) {
      const h2 = el.querySelector('#' + id)
      h2.tabIndex = -1
      return h2
    }

    // bucketAt is where a project folder in the archive was, on the computers that have it.
    function bucketAt(b) {
      for (const p of projects) {
        const at = (p.paths || []).filter((x) => slug(x.path) === b)
        if (at.length) return at
      }
      return []
    }

    // paintProfile shows how the computers' rules and skills compare: comparing (the
    // page asked, and /api/settings had no answer yet), the files that differ, or why not.
    function paintProfile() {
      const read = profileState === 'read'
      const k = read ? diffs.length : v.profileDiffs
      const line = slot('profile-line')
      line.classList.toggle('comparing', profileState === 'comparing' && k < 0)
      line.textContent = k < 0 ? (profileState === 'failed' ? "The rules and skills on your computers can't be compared right now." : 'Comparing the rules and skills on your computers…')
        : k ? `${plural(k, 'file')} ${k === 1 ? 'differs' : 'differ'} between your computers. Each keeps its own until you choose.`
          : 'Your computers have the same rules and skills.'
      const newer = (d) => (!d.newest ? 'copies differ' : same(d.newest, me) ? 'newer here' : 'newer on ' + d.newest)
      slot('profile').replaceChildren(...diffs.slice(0, 3).map((d) => h('li', null, code(d.file), ', ', newer(d))),
        ...(diffs.length > 3 ? [h('li', null, `and ${number(diffs.length - 3)} more`)] : []),
        ...(k > 0 && profileState === 'failed' ? [h('li', null, "The files can't be listed right now.")] : []))
      slot('profile').hidden = !slot('profile').children.length
      slot('compare').disabled = !read || !diffs.length // Compare lists what /api/profile gave
      slot('take-newer').disabled = !(k > 0)
    }

    function paint() {
      const next = v.problem ? noArchive(v.problem, () => reload(() => shownCard.querySelector('button'))) : card
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
      slot('computers').replaceChildren(...(computers.length ? computers.map((c, i) => h('tr', null,
        h('td', null, h('strong', { class: 'medium' }, c.name), c.this && [' ', h('span', { class: 'muted' }, '(this computer)')]),
        h('td', null, c.version || 'Unknown'),
        h('td', null, c.this ? day(c.lastPush) : about(c.lastPush)), // this computer's is its log's exact time
        h('td', null, c.ready ? h('span', { class: 'status ok' }, icon(14, ...OK), 'Ready') : h('span', { class: 'status warn' }, icon(14, ...WAIT), 'Needs update')),
        h('td', { class: 'r' }, !c.this && button('Forget', (e) => forget(c.name, e.currentTarget), { 'aria-label': 'Forget ' + c.name }))))
        : [h('tr', null, h('td', { colspan: 5, class: 'muted' }, 'No computer has backed up to this archive yet.'))]))

      slot('work').checked = !!cfg.workOn
      const folders = v.workFolders || []
      slot('folders').replaceChildren(...(folders.length ? folders.map((f) => h('tr', null,
        h('td', null, code(tilde(f.path))),
        h('td', { class: 'r' }, number(f.files)),
        h('td', { class: 'r' }, size(f.bytes || 0)),
        h('td', { class: 'r' }, button('Exclude', (e) => exclude(f.path, e.currentTarget), { 'aria-label': 'Exclude ' + tilde(f.path) }))))
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

      const saved = cfg.sweepEvery || 30, shown = pendingEvery ?? saved
      every.options = [...EVERY, ...[saved, shown].filter((n, i, a) => !EVERY.some(([m]) => m === n) && a.indexOf(n) === i).map((n) => [n, `Every ${n} minutes`])]
        .map(([value, label]) => ({ value, label }))
      every.value = shown
      hintEvery()

      paintProfile()

      // Provider wording only when the provider is known (Ruling P25).
      const where = v.archive && v.archive.provider ? `in ${v.archive.provider}, on your phone too` : 'in your sync folder'
      const pg = v.pages || {}
      slot('pages-line').textContent = `Every conversation gets a page you can read ${where}. ` +
        (!pg.pages ? 'No pages yet.' : pg.stale ? `${plural(pg.pages, 'page')}, some behind the newest conversations.` : `${plural(pg.pages, 'page')}, all up to date.`)
      const left = cfg.renderExclude || []
      slot('left-out').replaceChildren(...(left.length ? left.flatMap((x, i) => [i ? ', ' : '', code(x)]) : ['none']))

      // Oldest first, as in the file and as the full check writes; the newest is in view.
      pre.textContent = !log ? "The log can't be read right now." : log.length ? log.join('\n') : 'Nothing in the log yet.'
      setTimeout(() => { pre.scrollTop = pre.scrollHeight }) // after the router shows the screen
    }

    // Only Set up again applies a new Scheduled backup choice: say so while one differs from the saved.
    function hintEvery() {
      const on = pendingEvery != null && pendingEvery !== (cfg.sweepEvery || 30)
      if (on === !everyHint.hidden) return
      everyHint.hidden = !on
      const text = everyHint.querySelector('span')
      text.textContent = ''
      if (on) setTimeout(() => { text.textContent = 'Not applied yet. Choose Set up again to use this schedule.' }) // after it is shown, so it is announced
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

    async function forget(name, btn) {
      if (!await confirmDialog({ title: `Forget ${name}?`, body: FORGET, confirm: `Forget ${name}` })) return
      const row = [...slot('computers').querySelectorAll('button')].indexOf(btn)
      if (!await run(btn, 'machines-forget', { name }, `Forgetting ${name}…`)) return
      toast(`Forgot ${name}. Its conversations stay in the archive.`)
      // The next computer's Forget, the last one, or the section's heading.
      await reload(() => { const all = slot('computers').querySelectorAll('button'); return all[Math.min(row, all.length - 1)] || heading('comp-h') })
    }

    async function exclude(path, btn) {
      const shown = tilde(path)
      if (!await confirmDialog({ title: `Exclude ${shown}?`, body: 'Its copy in the archive is deleted. The folder on this computer is not touched.', confirm: 'Exclude', danger: true })) return
      if (!await config(btn, `Excluding ${shown}…`, 'exclude-folder', path)) return
      toast(`Excluded ${shown}.`)
      await reload(slot('exclude'))
    }

    async function include(path, btn) {
      if (!await config(btn, `Including ${tilde(path)} again…`, 'unexclude-folder', path)) return
      toast(`Included ${tilde(path)} again.`)
      await reload(slot('exclude'))
    }

    async function unmap(action, arg, shown, btn) {
      if (!await config(btn, 'Removing…', action, arg)) return
      toast(`Removed ${shown}. Bring in finds its place again.`)
      await reload(slot('add-folder'))
    }

    await load()

    renameBtn.addEventListener('click', rename)
    nameInput.addEventListener('input', () => { renameBtn.disabled = !nameInput.value.trim() || nameInput.value.trim() === me })
    nameInput.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !renameBtn.disabled && !busy(renameBtn)) rename() })
    // Any name goes to the CLI, which decides; a refusal shows its sentence (Ruling P9).
    // The old name stays in the archive as another computer: offer to forget it, behind Forget's own confirm.
    async function rename() {
      const old = me
      if (!await config(renameBtn, 'Renaming…', 'set-machine', nameInput.value.trim())) return
      await reload(nameInput)
      nameInput.value = me // the name as saved, which the CLI may have tidied
      renameBtn.disabled = true
      const forgetOld = () => [...slot('computers').querySelectorAll('button')].find((b) => b.getAttribute('aria-label') === 'Forget ' + old)
      if (!old || same(old, me) || !forgetOld()) return toast('Renamed. The next backup uses the new name.')
      toast(`Renamed. The next backup uses the new name; ${old} stays listed until you forget it.`, {
        action: { label: 'Forget ' + old, run: () => { const b = forgetOld(); if (b) { b.focus(); forget(old, b) } } },
      })
    }

    // The new folder is confirmed with the old one, a detected one too (Ruling P62).
    changeFolder.addEventListener('click', async (e) => {
      const btn = e.currentTarget
      const path = await chooseArchive(v.archive ? v.archive.path : null, tilde)
      if (!path) return
      const st = await config(btn, 'Changing the archive folder…', 'set-destination', path)
      if (!st) return
      toast(ownFolder(st, tilde) || 'Changed the archive folder.')
      await reload(btn)
    })

    // Turning work folders off needs no confirm: it deletes nothing (Ruling P63).
    const work = slot('work')
    work.addEventListener('change', async () => {
      const on = work.checked
      if (await config(work, on ? 'Turning work folders on…' : 'Turning work folders off…', 'work', on ? 'on' : 'off')) {
        toast(on ? 'Backing up the folders Claude works in.' : 'Stopped backing up the folders Claude works in.')
      }
      await reload(work)
    })
    for (const r of [slot('placement-home'), slot('placement-machine')]) {
      r.addEventListener('change', async () => {
        if (await config(r, 'Saving…', 'work-placement', r.value)) {
          toast(r.value === 'machine' ? "Other computers' work folders go to exactly the same path here." : "Other computers' work folders go to the same place in your home folder.")
        }
        await reload(r)
      })
    }
    slot('exclude').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      const path = await chooseFolder({ title: 'Choose a folder to exclude' })
      if (path) exclude(path, btn)
    })
    slot('scan-history').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      if (!await confirmDialog({ title: 'Look through older conversations?', body: SCAN, confirm: 'Look through older conversations' })) return
      const st = await run(btn, 'push', { scanHistory: true }, 'Looking through older conversations…')
      if (!st) return
      toast(`Looked through older conversations and backed up ${plural((st.result && st.result.copied) || 0, 'changed file')}.`)
      await reload(btn)
    })

    // The other computers' work folders, by the path each had there, from their records.
    slot('add-folder').addEventListener('click', async (e) => {
      const btn = e.currentTarget, byPath = new Map()
      for (const f of v.otherWorkFolders || []) byPath.set(f.path, [...(byPath.get(f.path) || []), f.machine])
      const options = [...byPath].map(([p, ms]) => ({ value: p, label: `${p} (${ms.join(', ')})` }))
      if (!options.length) return toast('Your other computers have no work folders in the archive.')
      const added = await mapDialog('Add a folder', 'A work folder on another computer', options,
        (from, to, b, error) => run(b, 'config', { action: 'map-folder', args: [from, to] }, 'Adding…', { error }))
      if (!added) return
      toast(`Added. ${added.from} goes to ${tilde(added.to)}.`)
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
      const added = await mapDialog('Add a project', 'A project from another computer', options,
        (from, to, b, error) => run(b, 'config', { action: 'map', args: [from, to] }, 'Adding…', { error }))
      if (!added) return
      toast(`Added. ${(bucketAt(added.from)[0] || { path: added.from }).path} goes to ${tilde(added.to)}.`)
      await reload(btn)
    })

    // Only after replies is install --no-sweep, which also removes the sweep (Rulings P40, P41).
    slot('install').addEventListener('click', async (e) => {
      const btn = e.currentTarget, n = every.value
      const st = await run(btn, 'install', n === -1 ? { noSweep: true } : { every: n }, 'Setting up automatic backup…')
      if (!st) return
      pendingEvery = null
      toast(st.result && st.result.sweepFailed ? "Set up again, but the scheduled backup didn't change. The details are in the log."
        : 'Set up automatic backup again. Restart Claude Code to use it.')
      await reload(btn)
      loadHealth()
    })
    slot('uninstall').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      if (!await confirmDialog({ title: 'Turn off automatic backup?', body: TURN_OFF, confirm: 'Turn off automatic backup', danger: true })) return
      if (!await run(btn, 'uninstall', {}, 'Turning off automatic backup…')) return
      toast('Turned off automatic backup. Your archive is kept.')
      await reload(btn)
      loadHealth()
    })

    slot('compare').addEventListener('click', () => modal('Compare rules and skills', null, (box, done) => {
      const close = h('button', { type: 'button', class: 'btn', onclick: () => done(null) }, 'Close')
      box.append(h('p', { class: 'muted' }, "Each file whose copies differ, with every computer's copy. Nothing changes until you take the newer copies."),
        h('ul', { class: 'rows panel small compare-list', tabindex: '0', 'aria-label': 'Files that differ' }, diffs.map((d) => h('li', null, code(d.file),
          h('ul', { class: 'compare-copies' }, (d.copies || []).map((c) => h('li', null, h('span', { class: 'medium' }, c.machine), `: ${size(c.size)}, ${day(c.modTime)}`,
            same(c.machine, d.newest) && [' ', h('span', { class: 'badge green' }, 'Newest')])))))),
        h('div', { class: 'row end' }, close))
      return close
    }))
    slot('take-newer').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      if (!await confirmDialog({ title: 'Take the newer copies here?', confirm: 'Take the newer copies here',
        body: "This computer's CLAUDE.md, rules and skills are replaced by the newer copies from your other computers. This computer's own copies stay in the archive." })) return
      const st = await run(btn, 'import', { profileOnly: true }, 'Taking the newer copies…')
      if (!st) return
      const files = (st.result && st.result.files) || []
      toast(files.length ? `Took the newer copies of ${plural(files.length, 'file')} here. This computer's own copies stay in the archive.`
        : 'Nothing newer to take: this computer has the newest copies.')
      await reload(btn)
    })

    slot('change-left-out').addEventListener('click', async (e) => {
      const btn = e.currentTarget, left = cfg.renderExclude || []
      const out = new Set(left.flatMap((x) => leftOutBy(projects, x)))
      const open = projects.filter((p) => p.name && !out.has(p))
      const pick = await modal('Readable pages', null, (box, done) => {
        const field = h('div')
        const leave = h('button', { type: 'button', class: 'btn', disabled: true, onclick: () => done({ exclude: open.find((p) => p.key === c.value) }) }, 'Leave out')
        const c = combo(field, { label: 'Leave a project out of pages', options: open.map((p) => ({ value: p.key, label: p.name })), value: '', onChange: () => { leave.disabled = !c.value } })
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
      if (pick.include) {
        if (!await run(btn, 'render', { include: [pick.include] }, `Publishing pages for ${pick.include} again…`)) return
        toast(`Published pages for ${pick.include} again.`)
        return reload(btn)
      }
      // The name is left out wherever it appears, so first say what else that reaches.
      const name = pick.exclude.name, also = leftOutBy(projects, name).filter((p) => p !== pick.exclude)
      if (also.length && !await confirmDialog({ title: `Leave ${name} out of pages?`, confirm: `Leave out ${plural(also.length + 1, 'project')}`,
        body: `Pages are left out by name, on every computer. This also leaves out:`,
        items: also.map((p) => `${p.name}, ${(p.paths || [])[0] ? p.paths[0].path : (p.buckets || [])[0]}`) })) return
      if (!await run(btn, 'render', { exclude: [name] }, `Leaving ${name} out of pages…`)) return
      toast(`Left ${name} out of pages.`)
      await reload(btn)
    })
    slot('rebuild').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      if (!await run(btn, 'render', { force: true }, 'Rebuilding all pages…')) return
      toast('Rebuilt all pages.')
      await reload(btn)
    })

    slot('backup-preview').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      const st = await run(btn, 'backup', { dryRun: true }, 'Counting…')
      const r = st && st.result
      if (r) slot('backup-result').textContent = `${plural(r.files, 'file')}, ${plural(r.transcripts, 'conversation')}`
    })
    slot('backup').addEventListener('click', async (e) => {
      const btn = e.currentTarget
      const list = home ? await api.get('/api/folders').catch(() => null) : null
      const start = list && (list.entries || []).some((f) => f.name === 'claude-backups') ? home + sep + 'claude-backups' : home
      const dir = await chooseFolder({ title: 'Choose where to save the backup file', start })
      if (!dir) return
      const st = await run(btn, 'backup', { out: dir }, 'Creating the backup file…')
      if (!st) return
      toast(`Created backup file ${((st.result && st.result.target) || '').split(/[\\/]/).pop()}.`)
    })

    slot('open-log').addEventListener('click', (e) => run(e.currentTarget, 'open-log', {}, 'Opening the log file…'))
    // The full check is doctor with every check, as a job (Ruling P54); its report shows
    // where the log was until the page is read again.
    slot('full-check').addEventListener('click', async (e) => {
      const btn = e.currentTarget, status = btn.closest('section').querySelector('.job-status')
      btn.setAttribute('aria-disabled', 'true')
      status.textContent = 'Running the full check…'
      let st = null
      try {
        const { id } = await api.run('doctor', {})
        pre.textContent = ''
        st = await follow(id, { signal, onLine: (s) => { pre.append(s + '\n'); pre.scrollTop = pre.scrollHeight } })
      } catch (err) {
        toast(plain(err))
      }
      btn.removeAttribute('aria-disabled')
      status.textContent = ''
      if (!st) return
      if (!st.ok) return toast(st.error || GENERIC)
      const r = st.result || {}
      toast(r.failures || r.warnings ? `Full check done: ${plural(r.failures, 'problem')} and ${plural(r.warnings, 'warning')}. Its report is in the log box.`
        : 'Full check done: everything is in order.')
    })

    slot('about').replaceChildren(`claude-sessions ${v.version}. `,
      h('a', { href: README, target: '_blank', rel: 'noopener noreferrer' }, 'Read the guide', h('span', { class: 'sr' }, ' (opens a new tab)')), '. ',
      h('a', { href: '#/commands' }, 'Every command and where it is in the panel'), '.')
    // Stop answers "now", or "after" the running job (Ruling P12).
    const stop = slot('stop'), note = slot('stop-note')
    note.textContent = v.fromShortcut ? 'It stops by itself 10 minutes after you close this tab.' : 'Closing this tab leaves it running.'
    stop.addEventListener('click', async () => {
      stop.setAttribute('aria-disabled', 'true')
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
        stop.removeAttribute('aria-disabled')
        toast(GENERIC)
      }
    })

    loadHealth()
  },
})
