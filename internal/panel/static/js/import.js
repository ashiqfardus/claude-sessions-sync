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
const EMPTY = 'Nothing to bring in yet. Once your other computer backs up to this archive, its projects and folders show here.'
const ABSOLUTE = /^(~([\\/]|$)|\/|[A-Za-z]:[\\/]|\\\\)/

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

    const rowsEl = slot('rows'), result = slot('result'), restoreResult = slot('restore-result')
    const update = slot('update'), work = slot('work'), plugins = slot('plugins'), profile = slot('profile'), repoint = slot('repoint')
    const root = slot('root'), rootBtn = slot('root-choose'), bare = slot('bare-name'), pull = slot('pull')
    const bringBtn = slot('bring'), previewBtn = slot('preview'), bar = slot('progress')
    const source = slot('source'), projectPath = slot('project-path'), rewriteFrom = slot('rewrite-from')
    const restoreBtn = slot('restore'), restorePreviewBtn = slot('restore-preview')
    const depth = combo(slot('depth'), { options: ['1', '2', '3', '4', '5', '6'].map((v) => ({ value: v, label: v })), value: '3' })
    let busy = false, total = 0, refocus = ''

    // Pull matches no projects, so the controls for matching are off; fixing paths needs work folders.
    function sync() {
      root.disabled = rootBtn.disabled = depth.input.disabled = bare.disabled = pull.checked
      repoint.disabled = pull.checked || !work.checked
    }
    work.addEventListener('change', sync)
    pull.addEventListener('change', sync)
    sync()

    // options gives the action and its body for Bring in or Preview first.
    function options(dryRun) {
      const b = { dryRun, update: update.checked, noWork: !work.checked, noPlugins: !plugins.checked, updateProfile: profile.checked }
      if (pull.checked) return ['pull', b]
      return ['import', { ...b, repoint: !repoint.disabled && repoint.checked, includeBareName: bare.checked, searchRoot: root.value.trim(), depth: Number(depth.value) }]
    }

    // busy turns every action off while a job runs. Disabling the focused button drops
    // focus to the page, so it goes back to the button that was clicked.
    function setBusy(on, clicked) {
      busy = on
      for (const b of [bringBtn, previewBtn, restoreBtn, restorePreviewBtn, ...rowsEl.querySelectorAll('button')]) b.disabled = on
      if (!on && clicked && document.activeElement === document.body) clicked.focus()
    }

    const message = (...kids) => rowsEl.replaceChildren(h('tr', null, h('td', { colspan: 4, class: 'muted' }, ...kids)))
    const loading = (text) => message('Looking for your projects…', text && h('span', { class: 'block small' }, text))

    function whyOf(r) {
      if (r.kind !== 'work') return WHY[r.matchedBy] || '—'
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
      rowsEl.replaceChildren(...rows.map(row))
      const again = refocus && [...rowsEl.querySelectorAll('button')].find((b) => b.dataset.key === refocus)
      refocus = ''
      if (again && document.activeElement === document.body) again.focus()
    }

    // run starts an action and follows it; a 409 or any refusal toasts its plain sentence.
    async function run(name, body, kind, clicked) {
      setBusy(true) // before the request, so a double click starts one job
      let id
      try {
        ({ id } = await api.run(name, body))
      } catch (e) {
        setBusy(false, clicked)
        if (signal.aborted) return
        toast(plain(e))
        if (kind === 'preview') message('Preview first to see exactly what changes.')
        return
      }
      watch(id, kind, name, clicked)
    }

    // watch follows a job to its end: this screen's own, or another action, which only
    // keeps the buttons off until it ends and then previews.
    async function watch(id, kind, action, clicked) {
      setBusy(true)
      if (kind !== 'other') remember(id + ' ' + kind)
      const bringing = kind === 'bring', counted = bringing && action !== 'pull' && total > 0
      let done = 0
      if (kind === 'preview') loading('')
      if (kind.startsWith('restore')) restoreResult.textContent = kind === 'restore' ? 'Filing them here…' : 'Checking the files…'
      if (bringing) {
        bringBtn.textContent = 'Bringing in'
        progress(0, !counted)
      }
      const onLine = (line) => {
        if (kind === 'preview' && line.startsWith(SLOW)) loading(SLOW_TEXT)
        const m = counted && FILED.exec(line)
        if (m) progress(done += Number(m[1]) + Number(m[2] || 0), false)
      }
      let st
      try {
        st = await follow(id, { signal, onLine, onProgress: kind === 'preview' ? loading : null })
      } catch (e) {
        st = { ok: false, error: plain(e) }
      }
      if (!st) return // the screen has gone; the job goes on, and the rail shows it
      remember('')
      if (bringing) {
        bringBtn.textContent = 'Bring in'
        bar.hidden = true
      }
      setBusy(false, clicked)
      if (kind === 'other') return begin()
      if (kind.startsWith('restore')) return restored(st, kind === 'restore-preview')

      const rows = Array.isArray(st.result) ? st.result : []
      const n = sum(rows, brought), w = rows.filter((r) => r.kind === 'work').length
      if (!st.ok) {
        // A failure says what was done before it (Ruling P44).
        if (kind === 'preview') {
          rowsEl.replaceChildren(h('tr', null, h('td', { colspan: 4 }, h('div', { class: 'row' },
            h('span', { role: 'alert' }, st.error || GENERIC), h('button', { type: 'button', class: 'btn sm', onclick: () => preview(previewBtn) }, 'Try again')))))
          result.textContent = ''
        } else result.textContent = (n || w ? `Brought in ${what(n, w)}. ` : '') + (st.error || GENERIC)
        return
      }
      table(rows)
      if (kind === 'preview') {
        total = n
        result.textContent = rows.length ? `Would bring in ${what(n, w)}.` + unchanged(sum(rows, (r) => r.skipped || 0)) : ''
        return
      }
      const skipped = rows.filter((r) => r.kind !== 'work' && MISSING.includes(r.matchedBy)).length
      result.textContent = skipped ? `Done. ${count(skipped, 'project')} ${skipped === 1 ? 'was' : 'were'} skipped until you choose a folder.`
        : 'Done. Everything is filed under your projects here.'
      toast('Brought in ' + what(n, w), { action: { label: 'See them', run: () => { location.hash = '#/conversations' } } })
    }

    // progress shows Bring in's bar: done of the last preview's total, or moving stripes
    // when there is no total to count against.
    function progress(done, indeterminate) {
      bar.hidden = false
      const pct = indeterminate ? 100 : Math.min(100, Math.round((done / total) * 100))
      bar.firstElementChild.style.width = pct + '%'
      if (indeterminate) bar.removeAttribute('aria-valuenow')
      else bar.setAttribute('aria-valuenow', pct)
      result.textContent = indeterminate ? 'Bringing in…' : `${number(Math.min(done, total))} of ${count(total, 'conversation')}`
    }

    function restored(st, dryRun) {
      const r = st.result || {}
      const n = (r.copied || 0) + (r.updated || 0)
      if (!st.ok) restoreResult.textContent = (n ? `Filed ${count(n, 'conversation')}. ` : '') + (st.error || GENERIC)
      else restoreResult.textContent = (dryRun ? `Would file ${count(n, 'conversation')} here.` : `Filed ${count(n, 'conversation')} here.`) + unchanged(r.skipped || 0)
    }

    const preview = (clicked) => { const [name, body] = options(true); run(name, body, 'preview', clicked) }
    bringBtn.addEventListener('click', () => { const [name, body] = options(false); run(name, body, 'bring', bringBtn) })
    previewBtn.addEventListener('click', () => preview(previewBtn))

    // Change and Choose folder save where the project or folder goes, then preview again.
    async function change(r) {
      const isWork = r.kind === 'work', from = r.recordedPath || r.bucket
      const folder = await chooseFolder({ title: 'Choose the folder for ' + from, start: r.localPath || undefined })
      if (!folder || signal.aborted) return
      refocus = key(r)
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
        const again = [...rowsEl.querySelectorAll('button')].find((b) => b.dataset.key === refocus)
        refocus = ''
        if (again) again.focus()
        return
      }
      if (isWork) { const s = await api.get('/api/settings').catch(() => null); if (s) useSettings(s) } // the row's Why follows the new map
      preview(null)
    }

    rootBtn.addEventListener('click', async () => {
      const v = root.value.trim()
      const folder = await chooseFolder({ title: 'Look for your projects in', start: /^(\/|[A-Za-z]:)/.test(v) ? v : undefined })
      if (folder && !signal.aborted) root.value = folder
    })

    function restore(dryRun) {
      const b = { source: source.value.trim(), projectPath: projectPath.value.trim(), rewriteFrom: rewriteFrom.value.trim(),
        includeBareName: slot('restore-bare-name').checked, dryRun, update: slot('restore-update').checked }
      if (!b.source || !b.projectPath) {
        restoreResult.textContent = 'Fill in the folder with the conversation files and the project they belong to.'
        return (b.source ? projectPath : source).focus()
      }
      if (b.rewriteFrom && !ABSOLUTE.test(b.rewriteFrom)) {
        restoreResult.textContent = 'The old path must be a full path, such as /Users/old-name/dev/web-app.'
        return rewriteFrom.focus()
      }
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
      watch(job.id, known ? kind : 'other', job.action, null)
    } else begin()
    return summary
  },
})
