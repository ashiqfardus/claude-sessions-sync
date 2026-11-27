// The Bring in screen (Import.dc.html): where each project and work folder from the
// other computers goes, with Change or Choose folder; what to bring in; Bring in and
// Preview first (import, or pull to keep their folder names); and From a folder
// instead (restore). Paths, names and log lines come from the archive: they reach the
// page only through h().
import { h, api, follow, toast, register, GENERIC } from './app.js'
import { combo } from './combo.js'
import { chooseFolder } from './folders.js'

// Every matchedBy import and pull give, in plain words (Ruling P38).
const WHY = {
  'git remote': 'Same git repository',
  'home folder': 'Same place in your home folder',
  'manual map': 'Your folder setting',
  'recorded path': 'The same path',
  'folder name': 'Same folder name',
  'work folder': 'Inside a work folder',
  'bucket name': 'Same folder name in the archive',
}
// Projects nothing here matched: skipped until a folder is chosen for them.
const MISSING = ['not found', 'ambiguous', 'mapped folder missing']
const KINDS = ['preview', 'bring', 'restore-preview', 'restore']
const JOB = 'cs.importJob' // this screen's running job and its kind, so a reload re-attaches as the same kind
// shortcut: import's own lines, pinned by its goldens, give the progress; pull prints none, so its bar is indeterminate.
const FILED = /^[+=] .*: (\d+) filed, \d+ already present(?:, (\d+) updated)?$/
// doctor's streamingAdvice names sync apps; the page says it without them (Ruling P25).
const SLOW = '! Your archive folder downloads files only'
const SLOW_TEXT = "Your archive folder downloads files only when they're opened, so this can take a while."
// Said under a preview that has given no progress for SLOW_AFTER ms; doctor's line, when it comes, says it for sure.
const SLOW_WAIT = "If your archive folder downloads files only when they're opened, this can take a few minutes."
const SLOW_AFTER = 10000
const LOOKING = 'Looking for your projects…'
const STALE = 'Options changed: preview again.'
const EMPTY = 'Nothing to bring in yet. Once your other computer backs up to this archive, its projects and folders show here.'
// Every path the panel sends is a full one, or starts with ~ (Ruling P59).
const FULL = /^(~([\\/]|$)|\/|[A-Za-z]:[\\/]|\\\\)/

const number = (n) => n.toLocaleString('en-GB')
const count = (n, one) => number(n) + ' ' + one + (n === 1 ? '' : 's')
const plain = (e) => (e && e.plain ? e.message : GENERIC)
const sum = (rows, f) => rows.reduce((n, r) => n + f(r), 0)
const brought = (r) => (r.kind === 'work' ? 0 : (r.filed || 0) + (r.updated || 0))
const what = (n, w) => count(n, 'conversation') + (w ? ' and ' + count(w, 'work folder') : '')
const unchanged = (k) => (k ? ` ${number(k)} ${k === 1 ? 'is' : 'are'} already here and unchanged.` : '')
const key = (r) => [r.kind, r.machine, r.bucket, r.recordedPath].join('|')
const remember = (v) => { try { sessionStorage.setItem(JOB, v) } catch { /* a reload then waits for it without its progress */ } }

// within says whether p is dir or inside it, ignoring case, as the work-folder rules do.
function within(p, dir) {
  const a = String(p).toLowerCase(), b = String(dir).toLowerCase().replace(/[\\/]+$/, '')
  return a === b || a.startsWith(b + '/') || a.startsWith(b + '\\')
}

register('import', {
  async mount(el, params, signal) {
    const slot = (name) => el.querySelector(`[data-slot="${name}"]`)
    const [settings, summary, state] = await Promise.all([api.get('/api/settings'), api.get('/api/summary'),
      api.get('/api/state').catch(() => null)])
    if (summary.problem || settings.problem) return { problem: summary.problem || settings.problem }

    let workMap = {}, placement = 'home'
    const useSettings = (s) => {
      workMap = (s && s.config && s.config.workMap) || {}
      placement = (s && s.config && s.config.placement) || 'home'
    }
    useSettings(settings)

    const rowsEl = slot('rows'), result = slot('result'), restoreResult = slot('restore-result'), live = slot('say')
    const update = slot('update'), work = slot('work'), plugins = slot('plugins'), profile = slot('profile'), repoint = slot('repoint')
    const root = slot('root'), rootBtn = slot('root-choose'), bare = slot('bare-name'), pull = slot('pull')
    const bringBtn = slot('bring'), previewBtn = slot('preview'), bar = slot('progress')
    const source = slot('source'), projectPath = slot('project-path'), rewriteFrom = slot('rewrite-from')
    const restoreBtn = slot('restore'), restorePreviewBtn = slot('restore-preview')
    let busy = false, refocus = '', shown = []
    // previewed is the last preview that succeeded: Bring in runs exactly it, and only
    // while the options still match it.
    let previewed = null, previewText = ''

    // say announces text in the screen's one live region; the same text twice is said twice.
    const say = (text) => { live.textContent = text + (live.textContent === text ? ' ' : '') }
    // show puts text in a result line: announced, or as an alert for a failure.
    function show(line, text, alert) {
      line.replaceChildren(alert ? h('span', { role: 'alert' }, text) : text)
      if (!alert && text) say(text)
    }

    // A field the server would refuse: marked, described by the message, and focused.
    const hints = new Map([root, source, projectPath, rewriteFrom].map((i) => [i, i.getAttribute('aria-describedby') || '']))
    function refuse(input, line, text) {
      input.setAttribute('aria-invalid', 'true')
      input.setAttribute('aria-describedby', (hints.get(input) + ' ' + line.id).trim())
      show(line, text, true)
      input.focus()
    }
    function clear(input) {
      input.removeAttribute('aria-invalid')
      if (hints.get(input)) input.setAttribute('aria-describedby', hints.get(input))
      else input.removeAttribute('aria-describedby')
    }
    for (const input of hints.keys()) input.addEventListener('input', () => clear(input))

    // Pull matches no projects, so the controls for matching are off; fixing paths needs work folders.
    function sync() {
      root.disabled = rootBtn.disabled = depth.input.disabled = bare.disabled = pull.checked
      repoint.disabled = pull.checked || !work.checked
    }

    // options gives the action and its body for Bring in or Preview first.
    function options() {
      const b = { dryRun: true, update: update.checked, noWork: !work.checked, noPlugins: !plugins.checked, updateProfile: profile.checked }
      if (pull.checked) return ['pull', b]
      return ['import', { ...b, repoint: !repoint.disabled && repoint.checked, includeBareName: bare.checked, searchRoot: root.value.trim(), depth: Number(depth.value) }]
    }
    const fresh = () => !!previewed && JSON.stringify(options()) === JSON.stringify([previewed.name, previewed.body])
    const gate = () => { bringBtn.disabled = busy || !fresh() }

    // changed runs after any option changes: Bring in waits for a preview of these options.
    function changed() {
      sync()
      gate()
      if (busy || !previewed) return // a running preview compares when it ends
      const text = fresh() ? previewText : STALE
      if (result.textContent !== text) show(result, text) // said once, not on every keystroke
    }
    const depth = combo(slot('depth'), { options: ['1', '2', '3', '4', '5', '6'].map((v) => ({ value: v, label: v })), value: '3', onChange: changed })
    for (const c of [update, work, plugins, profile, repoint, bare, pull]) c.addEventListener('change', changed)
    root.addEventListener('input', changed)
    sync()
    gate()

    // busy turns every action off while a job runs. Disabling the focused button drops
    // focus to the page, so it goes back to the button that was clicked.
    function setBusy(on, clicked) {
      busy = on
      for (const b of [previewBtn, restoreBtn, restorePreviewBtn, ...rowsEl.querySelectorAll('button')]) b.disabled = on
      gate()
      if (!on && clicked && document.activeElement === document.body) clicked.focus()
    }

    const message = (...kids) => { shown = []; rowsEl.replaceChildren(h('tr', null, h('td', { colspan: 4, class: 'muted' }, ...kids))) }

    function whyOf(r) {
      if (r.kind !== 'work') return r.matchedBy ? WHY[r.matchedBy] || 'Matched' : '—'
      if (Object.keys(workMap).some((k) => within(r.recordedPath, k))) return 'Your folder setting'
      return placement === 'machine' ? 'The same path' : 'Same place in your home folder'
    }

    function row(r) {
      const isWork = r.kind === 'work', missing = !isWork && MISSING.includes(r.matchedBy)
      const from = r.recordedPath || r.bucket || ''
      const n = (r.filed || 0) + (r.updated || 0) + (r.skipped || 0)
      const sub = [r.machine, isWork ? 'work folder' : n ? count(n, 'conversation') : ''].filter(Boolean).join(', ')
      const to = r.localPath || (r.matchedBy === 'bucket name' ? r.recordedPath : '')
      // A mapping changes nothing for pull's rows, which keep the archive's folder name, nor for a name config map refuses.
      const mappable = isWork ? !!r.recordedPath : !!r.matchedBy && r.matchedBy !== 'bucket name'
      return h('tr', { class: missing ? 'warn-row' : null },
        h('td', null, h('code', null, from), sub && h('span', { class: 'muted small block' }, sub)),
        missing ? h('td', { class: 'not-found' }, 'Not found on this computer') : h('td', null, to ? h('code', null, to) : '—'),
        h('td', { class: missing ? 'not-found-why' : null }, missing ? 'Skipped until you choose a folder' : whyOf(r)),
        h('td', { class: 'r' }, mappable && h('button', {
          type: 'button', class: missing ? 'btn sm dark' : 'btn sm', 'data-key': key(r), disabled: busy,
          'aria-label': (missing ? 'Choose folder: ' : 'Change: ') + from, onclick: () => change(r),
        }, missing ? 'Choose folder' : 'Change')))
    }

    function table(rows) {
      if (!rows.length) return message(EMPTY)
      shown = rows
      rowsEl.replaceChildren(...rows.map(row))
      const again = refocus && [...rowsEl.querySelectorAll('button')].find((b) => b.dataset.key === refocus)
      refocus = ''
      if (again && document.activeElement === document.body) again.focus()
    }

    // run starts an action and follows it. A refusal (a 409 among them) keeps the rows:
    // Bring in's is a toast, restore's goes under its own buttons.
    async function run(name, body, kind, clicked) {
      setBusy(true) // before the request, so a double click starts one job
      let id
      try {
        ({ id } = await api.run(name, body))
      } catch (e) {
        setBusy(false, clicked)
        if (signal.aborted) return
        if (kind.startsWith('restore')) return show(restoreResult, plain(e), true)
        toast(plain(e))
        if (kind === 'preview' && !shown.length) message('Preview first to see exactly what changes.')
        return
      }
      watch(id, kind, name, body, clicked)
    }

    // watch follows a job to its end: this screen's own, or another action, which only
    // keeps the buttons off until it ends and then previews. body is what was sent, when known.
    async function watch(id, kind, action, body, clicked) {
      setBusy(true)
      if (kind !== 'other') remember(id + ' ' + kind)
      const bringing = kind === 'bring', of = bringing && previewed ? previewed.total : 0
      const counted = bringing && action !== 'pull' && of > 0
      let done = 0, step = -1, slow = 0
      const unslow = () => { clearTimeout(slow); slow = 0 }
      if (kind === 'preview') {
        if (!shown.length) message(LOOKING)
        show(result, LOOKING)
        // Beside the line, the bar with no end: a preview can't count what it has yet to find.
        bar.hidden = false
        bar.setAttribute('aria-label', LOOKING)
        bar.removeAttribute('aria-valuenow')
        slow = setTimeout(() => {
          for (const line of shown.length ? [result] : [result, rowsEl.querySelector('td')]) line.append(h('span', { class: 'block small' }, SLOW_WAIT))
        }, SLOW_AFTER)
      }
      if (kind.startsWith('restore')) show(restoreResult, kind === 'restore' ? 'Filing them here…' : 'Checking the files…')
      // The bar and its line follow every project; the live region hears quarters only.
      const progress = () => {
        const pct = counted ? Math.min(100, Math.round((done / of) * 100)) : 100
        bar.hidden = false
        bar.style.setProperty('--p', pct / 100) // panel.css fills the bar to it while aria-valuenow is set
        if (counted) bar.setAttribute('aria-valuenow', pct)
        else bar.removeAttribute('aria-valuenow')
        const text = counted ? `${number(Math.min(done, of))} of ${count(of, 'conversation')}` : 'Bringing in…'
        result.replaceChildren(text)
        if (Math.floor(pct / 25) > step) { step = Math.floor(pct / 25); say(text) }
      }
      if (bringing) {
        bringBtn.textContent = 'Bringing in'
        bar.setAttribute('aria-label', 'Bringing in work')
        progress()
      }
      const onLine = (line) => {
        if (kind === 'preview' && line.startsWith(SLOW)) { unslow(); show(result, SLOW_TEXT) }
        const m = counted && FILED.exec(line)
        if (m) { done += Number(m[1]) + Number(m[2] || 0); progress() }
      }
      // Progress shows in the line and, while it has no rows, the table: not announced, only
      // seen. A count in it ("reading work folders: 40/200") fills the bar; otherwise it has no end.
      const onProgress = (text) => {
        unslow()
        result.replaceChildren(LOOKING + ' ' + text)
        if (!shown.length) message(LOOKING + ' ' + text)
        const m = /(\d+)\s*\/\s*(\d+)/.exec(text), pct = m && Number(m[2]) ? Math.min(100, Math.round((Number(m[1]) / Number(m[2])) * 100)) : null
        if (pct == null) bar.removeAttribute('aria-valuenow')
        else { bar.style.setProperty('--p', pct / 100); bar.setAttribute('aria-valuenow', pct) }
      }
      let st
      try {
        st = await follow(id, { signal, onLine, onProgress: kind === 'preview' ? onProgress : null })
      } catch (e) {
        st = { ok: false, error: plain(e) }
      }
      unslow()
      if (!st) return // the screen has gone; the job goes on, and the rail shows it
      remember('')
      if (bringing) bringBtn.textContent = 'Bring in'
      bar.hidden = true
      if (kind === 'preview') previewed = null // set again below when this one succeeded
      setBusy(false, clicked)
      if (kind === 'other') return begin()
      if (kind.startsWith('restore')) return restored(st, kind === 'restore-preview')

      const rows = Array.isArray(st.result) ? st.result : []
      const n = sum(rows, brought), w = rows.filter((r) => r.kind === 'work').length
      if (!st.ok) {
        // A failure says what was done before it (Ruling P44), and the rows shown stay.
        if (kind === 'preview' && !shown.length) {
          rowsEl.replaceChildren(h('tr', null, h('td', { colspan: 4 }, h('div', { class: 'row' },
            h('span', { role: 'alert' }, st.error || GENERIC), h('button', { type: 'button', class: 'btn sm', onclick: () => preview(previewBtn) }, 'Try again')))))
          result.replaceChildren()
        } else show(result, (bringing && (n || w) ? `Brought in ${what(n, w)}. ` : '') + (st.error || GENERIC), true)
        return
      }
      table(rows)
      if (kind === 'preview') {
        // A project not found here may be inside a work folder this brings in: import
        // matches it only once the folder is in place, so the run can bring in more.
        const later = w && rows.some((r) => r.kind !== 'work' && MISSING.includes(r.matchedBy))
        previewText = rows.length ? `Would bring in ${later ? 'at least ' : ''}${what(n, w)}.` + unchanged(sum(rows, (r) => r.skipped || 0)) : ''
        if (body) previewed = { name: action, body, total: n }
        gate()
        return show(result, !body || fresh() ? previewText : STALE)
      }
      const skipped = rows.filter((r) => r.kind !== 'work' && MISSING.includes(r.matchedBy)).length
      show(result, (skipped ? `Done. ${count(skipped, 'project')} ${skipped === 1 ? 'was' : 'were'} skipped until you choose a folder.`
        : 'Done. Everything is filed under your projects here.') + (previewed && !fresh() ? ' ' + STALE : ''))
      toast('Brought in ' + what(n, w), { action: { label: 'See them', run: () => { location.hash = '#/conversations' } } })
    }

    function restored(st, dryRun) {
      const r = st.result || {}
      const n = (r.copied || 0) + (r.updated || 0)
      if (!st.ok) show(restoreResult, (n ? `Filed ${count(n, 'conversation')}. ` : '') + (st.error || GENERIC), true)
      else show(restoreResult, (dryRun ? `Would file ${count(n, 'conversation')} here.` : `Filed ${count(n, 'conversation')} here.`) + unchanged(r.skipped || 0))
    }

    // preview runs the options as they are; their search folders must be full paths.
    function preview(clicked) {
      const [name, body] = options()
      if (name === 'import' && body.searchRoot && !body.searchRoot.split(',').every((p) => FULL.test(p.trim()))) {
        el.querySelector('details.advanced').open = true
        return refuse(root, result, 'Look for your projects in full paths, such as ~/dev or /Users/you/dev.')
      }
      run(name, body, 'preview', clicked)
    }
    // Bring in runs exactly what was previewed.
    bringBtn.addEventListener('click', () => { if (fresh()) run(previewed.name, { ...previewed.body, dryRun: false }, 'bring', bringBtn) })
    previewBtn.addEventListener('click', () => preview(previewBtn))

    // Change and Choose folder save where the project or folder goes, then preview again.
    async function change(r) {
      const isWork = r.kind === 'work', from = r.recordedPath || r.bucket
      const folder = await chooseFolder({ title: 'Choose the folder for ' + from, start: r.localPath || undefined })
      if (!folder || signal.aborted) return
      refocus = key(r)
      const back = () => {
        const again = [...rowsEl.querySelectorAll('button')].find((b) => b.dataset.key === refocus)
        refocus = ''
        if (again) again.focus()
      }
      if (!FULL.test(folder)) { toast('Choose a full path, such as ~/dev/web-app.'); return back() }
      setBusy(true)
      let st
      try {
        st = await api.run('config', { action: isWork ? 'map-folder' : 'map', args: isWork ? [r.recordedPath, folder] : [r.bucket, folder] }, { wait: true })
      } catch (e) {
        st = { ok: false, error: plain(e) }
      }
      setBusy(false)
      if (signal.aborted) return
      if (!st.ok) {
        toast(st.error || GENERIC)
        return back()
      }
      previewed = null // where things go has changed: Bring in waits for the preview below
      gate()
      if (isWork) { const s = await api.get('/api/settings').catch(() => null); if (s) useSettings(s) } // the row's Why follows the new map
      preview(null)
    }

    rootBtn.addEventListener('click', async () => {
      const v = root.value.trim()
      const folder = await chooseFolder({ title: 'Look for your projects in', start: /^(\/|[A-Za-z]:)/.test(v) ? v : undefined })
      if (!folder || signal.aborted) return
      root.value = folder
      clear(root)
      changed()
    })

    function restore(dryRun) {
      const b = { source: source.value.trim(), projectPath: projectPath.value.trim(), rewriteFrom: rewriteFrom.value.trim(),
        includeBareName: slot('restore-bare-name').checked, dryRun, update: slot('restore-update').checked }
      for (const i of [source, projectPath, rewriteFrom]) clear(i)
      if (!b.source) return refuse(source, restoreResult, 'Fill in the folder with the conversation files.')
      if (!FULL.test(b.source)) return refuse(source, restoreResult, 'Use a full path for the folder with the conversation files, such as ~/Downloads/old-backup/web-app.')
      if (!b.projectPath) return refuse(projectPath, restoreResult, 'Fill in the project they belong to here.')
      if (!FULL.test(b.projectPath)) return refuse(projectPath, restoreResult, 'Use a full path for the project, such as ~/dev/web-app.')
      if (b.rewriteFrom && !FULL.test(b.rewriteFrom)) return refuse(rewriteFrom, restoreResult, 'The old path must be a full path, such as /Users/old-name/dev/web-app.')
      run('restore', b, dryRun ? 'restore-preview' : 'restore', dryRun ? restorePreviewBtn : restoreBtn)
    }
    restoreBtn.addEventListener('click', () => restore(false))
    restorePreviewBtn.addEventListener('click', () => restore(true))

    // On open: a job already running (a reload, another tab or screen) is followed to its
    // end; otherwise the preview runs with the options as drawn.
    function begin() {
      if (!summary.conversations && !summary.workFolders) return message(EMPTY)
      preview(null)
    }
    const job = state && state.job && state.job.running ? state.job : null
    if (job) {
      let mine = ''
      try { mine = sessionStorage.getItem(JOB) || '' } catch { /* unknown: waited for as another action */ }
      const [id, kind] = mine.split(' ')
      const known = id === job.id && KINDS.includes(kind)
      if (!known) message(`Another action is running: ${job.label}. It will finish first.`)
      watch(job.id, known ? kind : 'other', job.action, null, null)
    } else begin()
    return summary
  },
})
