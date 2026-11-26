// Delete old conversations (Delete.dc.html): the age rule, a preview to choose from and
// delete now, Never delete, and the deleted list with Restore. Titles, project names,
// paths and computer names come from the archive: they reach the page only through h().
import { h, api, toast, confirmDialog, register, GENERIC } from './app.js'
import { combo } from './combo.js'

const number = (n) => n.toLocaleString('en-GB')
const plural = (n) => number(n) + ' conversation' + (n === 1 ? '' : 's')
const and = (names) => new Intl.ListFormat('en-GB', { type: 'conjunction' }).format(names)
const plain = (e) => (e && e.plain ? e.message : GENERIC)
const mb = (n) => (n / 2 ** 20).toFixed(1) + ' MB' // the CLI's own form
const titleOf = (it) => it.prompt || 'Conversation ' + it.id.slice(0, 8)
// nameOf is a deleted conversation's title, or its project when only that is known: the
// server reads the title from this computer's own copy, never the archive's.
const nameOf = (it) => (it.title && it.title !== 'Conversation ' + it.id.slice(0, 8) ? it.title
  : it.project ? 'Conversation in ' + it.project + ' (' + String(it.id || '').slice(0, 8) + ')' : it.here ? 'A conversation' : 'A conversation from another computer')
// refocus puts focus back on btn when disabling a button dropped it to the page.
const refocus = (btn) => { if (document.activeElement === document.body && btn.isConnected && !btn.hidden) btn.focus() }
const PAUSED = "Deleting is paused: the other computers can't be checked right now."

// The age rule's choices. config keep-days refuses anything else but off or 90 to
// 36500 days (archive.MinKeepDays, MaxKeepDays).
const AGES = [[90, '3 months'], [180, '6 months'], [365, '1 year'], [730, '2 years']]
const MIN_DAYS = 90, MAX_DAYS = 36500
const inRange = (d) => Number.isInteger(d) && d >= MIN_DAYS && d <= MAX_DAYS
const ageLabel = (d) => (AGES.find(([n]) => n === d) || [d, number(d) + ' days'])[1]
const ages = (days) => {
  const list = AGES.map(([d, label]) => ({ value: String(d), label }))
  // A setting made in the terminal that no choice has stays shown.
  return inRange(days) && !AGES.some(([d]) => d === days) ? [...list, { value: String(days), label: ageLabel(days) }].sort((a, b) => a.value - b.value) : list
}

// day is "14 Apr", or "14 Apr 2025" in an earlier year; '' for no date (Rulings P33, P43).
function day(t) {
  const d = t ? new Date(t) : null
  if (!d || isNaN(d) || d.getFullYear() < 2000) return ''
  const s = d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
  return d.getFullYear() === new Date().getFullYear() ? s : s + ' ' + d.getFullYear()
}

// run starts an action and waits for it, giving its final state; a refused request
// (409: another action is running) gives its plain message as the error.
async function run(name, body) {
  try {
    return await api.run(name, body, { wait: true })
  } catch (e) {
    return { ok: false, error: plain(e) }
  }
}

register('delete', {
  async mount(el, params, signal) {
    const slot = (name) => el.querySelector(`[data-slot="${name}"]`)
    const [first, deleted, projects, summary] = await Promise.all([
      api.get('/api/retention'),
      api.get('/api/deleted').catch((e) => ({ error: e })),
      api.get('/api/projects').catch(() => null),
      api.get('/api/summary').catch(() => null), // the archive's provider, and this computer's name
    ])
    if (first.problem) return first

    let retention = first
    const provider = (summary && summary.archive && summary.archive.provider) || ''
    const here = ((summary && summary.machine) || '').toLowerCase()
    // Provider wording only when the provider is known (Ruling P25).
    slot('provider').textContent = provider || 'your sync folder'
    slot('trash-note').hidden = provider !== 'Google Drive'
    // A project whose Arg is "" can't be named to keep or prune (Ruling P10).
    const choices = (Array.isArray(projects) ? projects : []).filter((p) => p.arg)
    // A preview item's project is a folder path: show its project's name, as the archive has it.
    const names = new Map()
    if (Array.isArray(projects)) for (const p of projects) for (const b of p.buckets || []) names.set(b, p.name)
    const projectOf = (it) => (it.buckets || []).map((b) => names.get(b)).find(Boolean) || (it.project || '').split(/[\\/]/).filter(Boolean).pop() || ''

    // --- Delete automatically ---------------------------------------------------

    let saved = inRange(first.keepDays) ? first.keepDays : 0
    const auto = combo(slot('auto-age'), { options: [{ value: '0', label: 'Never, keep everything' }, ...ages(saved)], value: String(saved) })
    const save = slot('save'), note = slot('auto-note')
    const grace = (days) => `Deleting conversations not used for ${ageLabel(days)} starts a day after each computer's next automatic backup.`
    // The note stays for the grace day after the setting changed; a server without keepDaysSetAt never shows it here.
    const setAt = Date.parse(first.keepDaysSetAt)
    if (saved && new Date(setAt).getFullYear() >= 2000 && Date.now() < setAt + 864e5) {
      note.hidden = false
      slot('auto-note-text').textContent = grace(saved)
    }
    save.addEventListener('click', async () => {
      const days = Number(auto.value)
      if (days && !inRange(days)) return // only the choices above can be picked
      save.disabled = true
      const st = await run('config', { action: 'keep-days', args: [days ? String(days) : 'off'] })
      save.disabled = false
      refocus(save)
      if (!st.ok) return toast(st.error || GENERIC)
      toast('Saved')
      if (days === saved) return
      saved = days
      // A new setting waits a day on every computer: the grace day is shared.
      note.hidden = !days
      slot('auto-note-text').textContent = ''
      if (days) setTimeout(() => { slot('auto-note-text').textContent = grace(days) })
    })

    // --- Choose now ------------------------------------------------------------

    // A preview is of the age and project it was asked with: changing either hides it.
    const stale = () => { if (shown) { shown = null; box.hidden = true } }
    const nowAge = combo(slot('now-age'), { options: ages(0), value: '180', onChange: stale })
    const nowProject = combo(slot('now-project'), { options: [{ value: '', label: 'Any project' }, ...choices.map((p) => ({ value: p.key, label: p.name }))], value: '', onChange: stale })
    // The note's preview is what the saved rule deletes: prune --older-than <days>d.
    slot('auto-preview').addEventListener('click', () => {
      if (!saved) return
      nowAge.options = ages(saved)
      nowAge.value = String(saved)
      nowProject.value = ''
      preview()
    })
    const show = slot('show'), box = slot('preview'), list = slot('items'), sum = slot('summary'), del = slot('delete')
    let shown = null // the preview: {sel, items, kept, plan}
    const off = new Set() // the ids unticked
    let deleting = '' // while the delete runs, what the summary says instead

    // pause is why deleting can't run, or '': a computer that would upload them again,
    // or holdouts that can't be checked (fail closed, as Conversations does).
    function pause() {
      const plan = shown && shown.plan
      if (!Array.isArray(retention.holdouts) || (plan && plan.unread)) return PAUSED
      const names = [...new Set([...retention.holdouts, ...((plan && plan.holdouts) || [])])]
      return names.length ? `Deleting waits until ${and(names)} ${names.length === 1 ? 'updates' : 'update'}.` : ''
    }

    // count fills the summary and the Delete button from the ticks.
    function count() {
      const on = shown.items.filter((it) => !off.has(it.id)), n = on.length, k = shown.kept
      const stays = k === 1 ? 'matches but is protected, so it stays' : 'match but are protected, so they stay'
      const text = shown.items.length ? `${plural(n)} will be deleted, ${mb(on.reduce((s, it) => s + it.size, 0))}.` + (k ? ` ${number(k)} more ${stays}.` : '')
        : k ? `Nothing would be deleted. ${number(k)} ${stays}.` : 'Nothing would be deleted: no conversation matches.'
      del.textContent = 'Delete ' + plural(n)
      del.hidden = !shown.items.length
      const why = pause()
      slot('wait-text').textContent = why
      slot('wait').hidden = !why || !shown.items.length
      del.disabled = !n || !!why || !!deleting
      if (why) del.setAttribute('aria-describedby', 'delete-wait')
      else del.removeAttribute('aria-describedby')
      return deleting || text
    }

    // The preview is prune without yes. Its age and project go with the delete too, so a
    // conversation continued since is not deleted.
    async function preview() {
      const key = nowProject.value, p = choices.find((c) => c.key === key)
      const sel = { olderThanDays: Number(nowAge.value), projects: p ? [p.arg] : [] }
      show.disabled = true
      const st = await run('prune', sel)
      show.disabled = false
      if (signal.aborted) return
      refocus(show)
      if (!st.ok || !st.result) return toast(st.error || GENERIC)
      const plan = st.result.plan
      // Oldest first: prune gives them in no useful order.
      const items = (plan.remove || []).slice().sort((a, b) => (Date.parse(a.lastAt) || 0) - (Date.parse(b.lastAt) || 0))
      shown = { sel, items, kept: plan.kept || 0, plan }
      off.clear()
      list.replaceChildren(...shown.items.map((it) => {
        const title = titleOf(it), date = day(it.lastAt)
        return h('li', null,
          h('input', { type: 'checkbox', class: 'check', checked: true, 'aria-label': 'Delete: ' + title, onchange: (e) => { if (e.target.checked) off.delete(it.id); else off.add(it.id); sum.textContent = count() } }),
          h('span', { class: 'grow' }, title),
          h('span', { class: 'muted small' }, [projectOf(it), date ? 'last used ' + date : 'no date'].filter(Boolean).join(', ')))
      }))
      list.hidden = !shown.items.length
      const text = count()
      sum.textContent = ''
      box.hidden = false
      setTimeout(() => { sum.textContent = text }) // after it is shown, so it is announced
    }
    show.addEventListener('click', preview)
    slot('cancel').addEventListener('click', () => { box.hidden = true; shown = null; show.focus() })

    del.addEventListener('click', async () => {
      const on = shown.items.filter((it) => !off.has(it.id)), ids = on.map((it) => it.id), label = 'Delete ' + plural(on.length)
      if (!ids.length) return
      const body = `They are removed from the archive in ${provider || 'your sync folder'}, from every computer's folder. Your computers keep their own copies.`
      if (!await confirmDialog({ title: label + '?', body, items: on.map(titleOf), confirm: label, danger: true })) return
      deleting = `Deleting ${plural(ids.length)}…`
      sum.textContent = count()
      const st = await run('prune', { ...shown.sel, ids, yes: true })
      deleting = ''
      // A run that fails partway still says what it deleted (Ruling P44).
      const n = (st.result && st.result.deleted) || 0, left = ids.length - n
      let text = n ? `Deleted ${plural(n)} from the archive` : 'Nothing was deleted'
      if (!st.ok) text = (n ? text + '. ' : '') + (st.error || GENERIC)
      // The run dates each again: one used since the preview is no longer old enough.
      else if (left > 0) text += `${n ? '. ' : ': '}${number(left)} ${left === 1 ? 'was' : 'were'} used since the preview, so ${left === 1 ? 'it stays' : 'they stay'}`
      const withRestore = n ? { action: { label: 'Restore', run: () => restoreAll(ids) } } : undefined
      if (signal.aborted) return toast(text, withRestore) // restoreAll repaints only a screen still shown
      box.hidden = true // the preview is out of date now
      shown = null
      reloadDeleted()
      toast(text, withRestore)
      // The toast is last in the page: put focus on Restore, so the keyboard can reach it.
      const undo = n && document.querySelector('#toasts .toast button')
      if (undo) undo.focus()
      else refocus(show)
    })

    async function restoreAll(ids) {
      const st = await run('prune-undo', { ids })
      if (!st.ok) toast(st.error || GENERIC)
      else toast(`Restored ${plural(st.result && Array.isArray(st.result.undone) ? st.result.undone.length : ids.length)}. Each comes back at the next backup of a computer that still has it.`)
      if (signal.aborted) return
      if (st.ok) await reloadDeleted()
      // Focus left with the toast's Restore button: it goes to the list Restore changed, not the page.
      if (document.activeElement === el || document.activeElement === document.body) goneHeading.focus()
    }

    // --- Never delete ----------------------------------------------------------

    const keptList = slot('kept'), protectBtn = slot('protect')
    const protect = combo(slot('keep-project'), { options: [], value: '' })

    // where is a project's folders by computer: "<a> here, <b> and <c> on LAP-2".
    function where(paths) {
      const by = new Map()
      for (const p of paths) by.set(p.machine, [...(by.get(p.machine) || []), p.path])
      const groups = [...by].sort(([a], [b]) => (b.toLowerCase() === here) - (a.toLowerCase() === here))
      const out = []
      groups.forEach(([machine, ps], g) => {
        if (g) out.push(', ')
        ps.forEach((p, i) => { if (i) out.push(i === ps.length - 1 ? ' and ' : ', '); out.push(h('code', null, p)) })
        out.push(here && machine.toLowerCase() === here ? ' here' : ' on ' + machine)
      })
      return out
    }

    function paintKept() {
      const rows = Array.isArray(retention.keep) ? retention.keep : []
      // A list read in part must never read as "nothing is protected", nor be written to.
      const partial = retention.unreadable > 0
      slot('keep-unread').hidden = !partial
      for (const b of [save, protectBtn]) {
        b.disabled = partial
        if (partial) b.setAttribute('aria-describedby', 'keep-unread')
        else b.removeAttribute('aria-describedby')
      }
      keptList.replaceChildren(...(rows.length ? rows.map((r) => h('li', null,
        h('span', { class: 'grow' }, h('strong', { class: 'medium' }, r.name), ' ',
          h('span', { class: 'muted' }, r.kind === 'project' ? 'project' : 'conversation', r.kind === 'project' && r.paths && r.paths.length ? [': ', ...where(r.paths)] : null)),
        h('button', { type: 'button', class: 'btn sm', 'aria-label': 'Stop protecting: ' + r.name, disabled: !r.arg, onclick: (e) => unkeep(r, e.currentTarget) }, 'Stop protecting')))
        : partial ? [] : [h('li', { class: 'muted' }, 'Nothing is protected yet.')]))
      const taken = new Set(rows.filter((r) => r.kind === 'project').map((r) => r.arg))
      protect.options = [{ value: '', label: 'Choose a project' }, ...choices.filter((p) => !taken.has(p.arg)).map((p) => ({ value: p.key, label: p.name }))]
      protect.value = ''
    }

    async function reloadRetention() {
      let v
      try { v = await api.get('/api/retention') } catch (e) { return toast(plain(e)) }
      if (signal.aborted || v.problem) return
      retention = v
      paintKept()
      if (shown) sum.textContent = count() // the holdouts may have changed
    }

    async function unkeep(r, btn) {
      const i = [...keptList.querySelectorAll('button')].indexOf(btn)
      btn.disabled = true
      const st = await run('config', { action: 'unkeep', args: [r.arg] })
      if (!st.ok) { btn.disabled = false; refocus(btn); return toast(st.error || GENERIC) }
      toast('Stopped protecting ' + r.name)
      await reloadRetention()
      const left = keptList.querySelectorAll('button')
      if (!signal.aborted) (left[i] || left[i - 1] || protectBtn).focus()
    }

    protectBtn.addEventListener('click', async () => {
      const p = choices.find((c) => c.key === protect.value)
      if (!p) return protect.input.focus() // opens the list to choose from
      protectBtn.disabled = true
      const st = await run('config', { action: 'keep', args: [p.arg] })
      protectBtn.disabled = retention.unreadable > 0
      refocus(protectBtn)
      if (!st.ok) return toast(st.error || GENERIC)
      toast('Protected ' + p.name)
      await reloadRetention()
    })

    // --- Deleted recently ------------------------------------------------------

    const goneList = slot('deleted'), goneHeading = el.querySelector('#gone-h')
    goneHeading.tabIndex = -1 // where focus goes after a Restore from the toast

    // paintDeleted takes the list, {problem} or {error} (Ruling P56).
    function paintDeleted(items) {
      if (!Array.isArray(items)) return goneList.replaceChildren(h('li', { class: 'muted' }, items && items.error ? plain(items.error) : GENERIC))
      goneList.replaceChildren(...(items.length ? items.map((it) => {
        const d = day(it.deletedAt), name = nameOf(it)
        return h('li', null,
          h('span', { class: 'grow' }, h('strong', { class: 'medium' }, name), ' ',
            h('span', { class: 'muted' }, 'deleted' + (d ? ' ' + d : '') + '.' + (it.here ? ' This computer still has it.' : ''))),
          h('button', { type: 'button', class: 'btn sm', 'aria-label': 'Restore: ' + name, onclick: (e) => restore(it, e.currentTarget) }, 'Restore'))
      }) : [h('li', { class: 'muted' }, 'Nothing deleted recently.')]))
    }

    async function reloadDeleted() {
      const v = await api.get('/api/deleted').catch((e) => ({ error: e }))
      if (!signal.aborted) paintDeleted(v)
    }

    async function restore(it, btn) {
      const i = [...goneList.querySelectorAll('button')].indexOf(btn)
      btn.disabled = true
      btn.textContent = 'Restoring'
      const st = await run('prune-undo', { ids: [it.id] })
      if (!st.ok) {
        btn.disabled = false
        btn.textContent = 'Restore'
        refocus(btn)
        return toast(st.error || GENERIC)
      }
      toast(`${nameOf(it)} comes back at ${it.here ? "this computer's next backup" : 'the next backup of a computer that still has it'}`)
      await reloadDeleted()
      const left = goneList.querySelectorAll('button')
      if (!signal.aborted) (left[i] || left[i - 1] || el.querySelector('h1')).focus()
    }

    paintKept()
    paintDeleted(deleted)
    return first
  },
})
