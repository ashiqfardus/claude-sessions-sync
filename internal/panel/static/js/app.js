// The page core: DOM helper, API client, jobs, dialogs, toasts, router, heartbeat.
// Archive text is untrusted: it only ever reaches the page through textContent or
// createTextNode (h() below), never as HTML.
import { rail } from './rail.js'
import { chooseFolder } from './folders.js'

export const GENERIC = "That didn't work. The details are in the log on the Settings page."
const NO_ARCHIVE = 'No archive folder yet. Choose the folder your sync app keeps in sync.'
const UNREACHABLE = "The archive folder can't be reached right now. Check that your sync app is running, then try again."
const SVG_NS = 'http://www.w3.org/2000/svg'

// Each screen lives in js/<screen>.js and calls register('<screen>', ...) when the
// router first imports it. Each has a <template id="t-<screen>"> in index.html.
const SCREENS = ['overview', 'conversations', 'storage', 'delete', 'import', 'settings', 'commands']
// Screens without a rail link of their own mark their section's link, as the mockups do.
const SECTION = { delete: 'storage', import: 'overview' }

const token = document.querySelector('meta[name="cs-token"]').content
const app = document.querySelector('.app')
const main = document.getElementById('main')
const strip = document.querySelector('.working') // across the content area while a job runs
const nav = document.getElementById('rail')
const dialogs = document.getElementById('dialogs')

// README is the panel's one external link (the About link in Settings).
export const README = 'https://github.com/ashiqfardus/claude-sessions-sync#readme'
let readmeLinked = false

// Attributes that load or link elsewhere, refused outright (any name ending in src but
// src itself and data-* is refused too). href and src must resolve to this panel.
const REFUSED = ['style', 'srcdoc', 'ping', 'data', 'poster', 'background', 'manifest', 'codebase', 'archive', 'formaction', 'action', 'srcset', 'imagesrcset', 'xlink:href']
const CONTROL = /[\u0000-\u001F\u007F]/ // the URL parser drops tabs and newlines: "/\t/host" would become "//host"

// local says whether a URL resolves to this panel's own origin and scheme.
function local(value) {
  const s = String(value)
  if (CONTROL.test(s)) return false
  try {
    const u = new URL(s, location.href)
    return u.origin === location.origin && u.protocol === location.protocol
  } catch {
    return false
  }
}

// Settings calls allowExternal(README) once, so h() may then use it as an href. It
// throws for any other URL: no other site can ever be linked.
export function allowExternal(url) {
  if (!/^https:\/\//.test(url) || url !== README) throw new Error('h: only the README may be linked')
  readmeLinked = true
}

// h(tag, attrs, ...children). A function attribute named on<event> becomes a listener;
// true sets an empty attribute; false, null and undefined set nothing. Strings and
// numbers become text nodes; null and false children are skipped. Attribute names are
// matched in any case. It refuses `style` (the CSP drops it), every attribute that
// loads or links elsewhere, a string on<event>, and an href or src that resolves
// outside the panel, so archive text can never become a link or load elsewhere.
export function h(tag, attrs, ...kids) {
  const el = document.createElement(tag)
  for (const [k, raw] of Object.entries(attrs || {})) {
    const n = k.toLowerCase()
    if (raw == null || raw === false) continue
    if (REFUSED.includes(n) || (n.endsWith('src') && n !== 'src' && !n.startsWith('data-'))) throw new Error('h: refused ' + n)
    if (n.startsWith('on')) {
      if (typeof raw !== 'function') throw new Error('h: refused ' + n)
      el.addEventListener(n.slice(2), raw)
      continue
    }
    const v = raw === true ? '' : String(raw) // once: the text checked is the text set
    if ((n === 'href' || n === 'src') && !local(v) && !(n === 'href' && readmeLinked && v === README)) throw new Error('h: refused ' + n)
    el.setAttribute(n, v)
  }
  for (const kid of kids.flat()) {
    if (kid instanceof Node) el.append(kid)
    else if (kid != null && kid !== false) el.append(document.createTextNode(String(kid)))
  }
  return el
}

// icon(size, ...pathData) is a stroked 24x24 icon in the mockups' style, hidden from
// screen readers.
export function icon(size, ...paths) {
  const svg = document.createElementNS(SVG_NS, 'svg')
  const attrs = { width: size, height: size, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': 2, 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true' }
  for (const [k, v] of Object.entries(attrs)) svg.setAttribute(k, v)
  for (const d of paths) {
    const p = document.createElementNS(SVG_NS, 'path')
    p.setAttribute('d', d)
    svg.append(p)
  }
  return svg
}

// when(time) is "21:45" today, "Yesterday, 18:30", otherwise "18 Nov"; null for no time
// (Go's zero time included).
export function when(t) {
  const d = t ? new Date(t) : null
  if (!d || isNaN(d) || d.getFullYear() < 2000) return null
  const hm = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  const day = new Date()
  if (d.toDateString() === day.toDateString()) return hm
  day.setDate(day.getDate() - 1)
  if (d.toDateString() === day.toDateString()) return 'Yesterday, ' + hm
  return d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
}

const motion = matchMedia('(prefers-reduced-motion: reduce)')
// reducedMotion is live: JS-driven motion (Overview's count-up) skips itself when true.
export let reducedMotion = motion.matches
motion.addEventListener('change', (e) => { reducedMotion = e.matches })

// --- API ---------------------------------------------------------------------------

// Every error the API gives has a plain message (plain: true) that can be shown as is.
function fail(message, status) {
  const e = new Error(message)
  e.plain = true
  e.status = status
  return e
}

async function call(path, init) {
  let r
  try {
    r = await fetch(path, { credentials: 'same-origin', ...init })
  } catch {
    // One failed request is not yet a stopped panel: the heartbeat decides, and shows the notice.
    if (path !== '/api/heartbeat') await beat()
    throw fail(GENERIC, 0)
  }
  let body = null
  try { body = await r.json() } catch { /* an empty or non-JSON body */ }
  if (!r.ok) throw fail(body && typeof body.error === 'string' ? body.error : GENERIC, r.status)
  return body
}

function post(path, body) {
  return call(path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CS-Token': token }, body: JSON.stringify(body || {}) })
}

function query(params) {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params || {})) for (const x of [].concat(v)) if (x != null) q.append(k, x)
  const s = q.toString()
  return s ? '?' + s : ''
}

const inflight = new Map()

// get is api.get without the busy button: the rail's own polling uses it.
function get(path, params) {
  const url = path + query(params)
  if (!inflight.has(url)) {
    if (path === '/api/summary') summaryAt = Date.now()
    inflight.set(url, call(url).then((v) => { if (path === '/api/summary') lastBackup(v); return v })
      .finally(() => inflight.delete(url)))
  }
  return inflight.get(url)
}

// --- Busy buttons ------------------------------------------------------------------

// Work a click starts shows on the button clicked: aria-busy, and panel.css's spinning
// ring in it, until that work ends; then a check for DONE ms when an action in it
// succeeded. A screen's request (api.get or api.run) within AFTER ms of the click, or of
// the end of earlier work it started, is its work: so a reload after an action, or the
// next job of a list, keeps the button busy too.
// shortcut: work that starts later than AFTER shows only in the strip and the rail foot.
const AFTER = 2000, DONE = 900
let pressed = null // {el, at, ok, failed}: the button the last click reached, and how its work went
const holds = new WeakMap() // how many pieces of work each busy button waits for
const checks = new WeakMap() // each button's timer that takes its check away
const jobs = new Map() // a started job's id -> {free, at}, until its end is seen
document.addEventListener('click', (e) => {
  const b = e.target instanceof Element && e.target.closest('button')
  pressed = b ? { el: b, at: Date.now() } : null
}, true) // capture: before any screen's handler starts the work

// hold marks the pressed button busy and gives free(ok), which ends this piece of work:
// ok is true for an action that succeeded, false for a failure, undefined for a read.
function hold() {
  const p = pressed, el = p && p.el
  if (!el || !el.isConnected || Date.now() - p.at > AFTER) return () => {}
  holds.set(el, (holds.get(el) || 0) + 1)
  el.removeAttribute('data-done')
  el.setAttribute('aria-busy', 'true')
  let on = true
  return (ok) => {
    if (!on) return
    on = false
    if (ok === false) p.failed = true
    else if (ok) p.ok = true
    const n = holds.get(el) - 1
    holds.set(el, n)
    if (n) return
    el.removeAttribute('aria-busy')
    if (pressed && pressed.el === el) pressed.at = Date.now()
    if (!p.ok || p.failed) return // a failure says the rest in its own words
    el.setAttribute('data-done', '')
    clearTimeout(checks.get(el))
    checks.set(el, setTimeout(() => el.removeAttribute('data-done'), DONE))
  }
}

// ended frees the button of a job whose end was seen; ok as for hold.
function ended(id, ok) {
  const j = jobs.get(id)
  if (j) { jobs.delete(id); j.free(ok) }
}

export const api = {
  // get('/api/summary', {page: 2}) gives the decoded JSON. Two callers asking for the
  // same URL at once share one request; every summary also updates the rail foot.
  get(path, params) {
    const p = get(path, params), free = hold()
    p.then(() => free(), () => free(false))
    return p
  },
  // run('push', {dryRun: true}) starts an action and gives {id}; with {wait: true} it
  // gives the job's final State. A running job answers 409 with a plain error. The
  // button clicked stays busy until the job ends: follow's done, the wait settling, or
  // poll finding it no longer running.
  run: (name, body, { wait } = {}) => {
    const free = hold()
    if (wait) setTimeout(poll, 500) // the request is held until the job ends; show it in the rail meanwhile
    return post('/api/run/' + encodeURIComponent(name) + (wait ? '?wait=1' : ''), body)
      .then((v) => { if (!wait && v && v.id) jobs.set(v.id, { free, at: Date.now() }); else free(!!(v && v.ok)); return v },
        (e) => { free(false); throw e })
      .finally(() => { poll() })
  },
}

function parse(text) {
  try { return JSON.parse(text) } catch { return undefined }
}

// follow(id, {onLine, onProgress, signal}) streams a job's lines and progress, and gives
// {id, ok, result, error, lines} when it is done. Aborting signal (the router aborts a
// screen's signal when it unmounts) closes the stream and gives null.
export function follow(id, { onLine, onProgress, signal } = {}) {
  return new Promise((resolve, reject) => {
    if (signal && signal.aborted) return resolve(null)
    const es = new EventSource('/api/jobs/events?id=' + encodeURIComponent(id))
    const lines = []
    const abort = () => { es.close(); resolve(null) }
    const end = () => { if (signal) signal.removeEventListener('abort', abort) }
    if (signal) signal.addEventListener('abort', abort)
    es.addEventListener('line', (e) => { const s = parse(e.data); if (typeof s !== 'string') return; lines.push(s); if (onLine) onLine(s) })
    es.addEventListener('progress', (e) => { const s = parse(e.data); if (typeof s === 'string' && onProgress) onProgress(s) })
    es.addEventListener('done', (e) => {
      es.close() // otherwise it reconnects every few seconds (Ruling P20)
      end()
      const d = parse(e.data)
      ended(id, !!(d && d.ok))
      poll()
      if (d && typeof d === 'object') resolve({ id, ...d, lines })
      else reject(fail(GENERIC))
    })
    es.onerror = () => {
      // CLOSED: the server refused the stream (an unknown job). Otherwise the browser is
      // reconnecting; a heartbeat tells a dropped stream from a stopped panel.
      if (es.readyState === EventSource.CLOSED) { end(); ended(id, false); return reject(fail(GENERIC, 404)) }
      beat().then((alive) => { if (!alive) { es.close(); end(); reject(fail(GENERIC, 0)) } })
    }
  })
}

// leave plays an element's way out (panel.css's .leaving: quicker than its way in,
// opacity only under reduced motion) and then removes it. It takes no clicks or focus
// meanwhile. Only its own and its first child's animations are waited for: a spinner
// inside never ends.
function leave(el) {
  if (el.classList.contains('leaving')) return
  el.inert = true
  el.classList.add('leaving')
  const going = [el, el.firstElementChild].flatMap((n) => (n ? n.getAnimations() : []))
  Promise.allSettled(going.map((a) => a.finished)).then(() => el.remove())
}

// --- Toasts ------------------------------------------------------------------------

let dropToast = () => {}

// toast(text, {action: {label, run}}): one at a time, for 4.5 s, in the #toasts live
// region. Hovering or focusing it holds it, so its button can always be reached.
export function toast(text, { action } = {}) {
  dropToast()
  let timer
  const el = h('div', { class: 'toast' }, h('span', null, text),
    h('button', { type: 'button', class: 'btn ghost sm', onclick: () => { close(); if (action) action.run() } }, action ? action.label : 'Dismiss'))
  const close = dropToast = () => {
    clearTimeout(timer)
    if (el.contains(document.activeElement)) main.focus()
    leave(el)
  }
  const arm = () => { if (el.isConnected) { clearTimeout(timer); timer = setTimeout(close, 4500) } }
  el.addEventListener('mouseenter', () => clearTimeout(timer))
  el.addEventListener('mouseleave', arm)
  el.addEventListener('focusin', () => clearTimeout(timer))
  el.addEventListener('focusout', arm)
  document.getElementById('toasts').append(el)
  // On a phone the ticked conversations' bar sits at the bottom too: the toast rises above
  // it, and comes down again if the bar goes while it shows (panel.css's --lift).
  const bar = document.querySelector('.selection:not([hidden])')
  if (bar && getComputedStyle(bar).position === 'fixed') {
    el.style.setProperty('--lift', parseFloat(getComputedStyle(bar).bottom) + bar.offsetHeight + 12 - parseFloat(getComputedStyle(el).bottom) + 'px')
  }
  arm()
}

// --- Dialogs -----------------------------------------------------------------------

let dialogN = 0
const openDialogs = new Set() // each open dialog's cancel, so navigation can close them
// top is the open dialog on top (its scrim), or null; one on its way out does not count.
const top = () => [...dialogs.children].filter((s) => !s.classList.contains('leaving')).pop() || null

// modal(title, cancelValue, build) opens a modal dialog. build(box, done) adds the
// content and returns the element to focus first; done(value) closes it. Esc gives
// cancelValue. Focus stays inside and goes back to the opener at the end.
export function modal(title, cancelValue, build) {
  return new Promise((resolve) => {
    const opener = document.activeElement
    // The button that opened it: the click that closes it hands the work it starts back to that button.
    const origin = opener instanceof HTMLButtonElement ? opener : pressed && pressed.el
    const id = 'dlg-' + ++dialogN
    const box = h('div', { class: 'dialog', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': id }, h('h2', { id }, title))
    const scrim = h('div', { class: 'scrim' }, box)
    const onKey = (e) => {
      if (top() !== scrim) return // a dialog opened from this one
      if (e.key === 'Escape') { e.preventDefault(); done(cancelValue) }
      else if (e.key === 'Tab') trap(e, box)
    }
    let closed = false
    const cancel = () => done(cancelValue)
    const done = (value) => {
      if (closed) return
      closed = true
      openDialogs.delete(cancel)
      document.removeEventListener('keydown', onKey)
      if (pressed && box.contains(pressed.el)) pressed = origin ? { el: origin, at: pressed.at } : null
      leave(scrim)
      if (!dead) (top() || app).inert = false
      if (opener && opener.isConnected && opener.focus) opener.focus()
      resolve(value)
    }
    const first = build(box, done)
    for (const el of [app, ...dialogs.children]) el.inert = true
    dialogs.append(scrim)
    openDialogs.add(cancel)
    document.addEventListener('keydown', onKey)
    ;(first || box.querySelector('button, input, a[href]')).focus()
  })
}

function trap(e, box) {
  const all = [...box.querySelectorAll('a[href], button, input, select, textarea, [tabindex]')]
    .filter((el) => !el.disabled && el.tabIndex >= 0 && el.getClientRects().length)
  if (!all.length) return
  const first = all[0], last = all[all.length - 1], at = document.activeElement
  if (!box.contains(at) || (e.shiftKey && at === first)) { e.preventDefault(); (e.shiftKey ? last : first).focus() }
  else if (!e.shiftKey && at === last) { e.preventDefault(); first.focus() }
}

// confirmDialog({title, body, items, confirm, danger}) gives true when confirmed.
// body is text or a node; items are listed; confirm is the button's label (the title
// by default), red when danger. Focus starts on Cancel.
export function confirmDialog({ title, body, items, confirm, danger }) {
  return modal(title, false, (box, done) => {
    const cancel = h('button', { type: 'button', class: 'btn', onclick: () => done(false) }, 'Cancel')
    if (body) {
      const id = box.getAttribute('aria-labelledby') + '-body'
      box.setAttribute('aria-describedby', id)
      box.append(h('p', { id, class: 'muted' }, body))
    }
    if (items && items.length) box.append(h('ul', { class: 'rows panel small dialog-items' }, items.map((t) => h('li', null, t))))
    box.append(h('div', { class: 'row end' }, cancel,
      h('button', { type: 'button', class: danger ? 'btn danger' : 'btn dark', onclick: () => done(true) }, confirm || title)))
    return cancel
  })
}

// --- No archive, loading and errors ------------------------------------------------

// noArchive(problem, retry) is the shared empty state for a view whose problem is
// "none" or "unreachable". Choose folder runs config set-destination, then reloads. An
// unreachable folder is most often a sync app not started yet, so there Try again comes
// first and a new folder is confirmed against the old one (Ruling P62).
export function noArchive(problem, retry) {
  const unreachable = problem === 'unreachable'
  return h('div', { class: 'panel pad stack' }, h('p', null, unreachable ? UNREACHABLE : NO_ARCHIVE),
    h('div', { class: 'row' },
      unreachable && retry && h('button', { type: 'button', class: 'btn primary', onclick: retry }, 'Try again'),
      h('button', { type: 'button', class: unreachable ? 'btn' : 'btn primary', onclick: () => setArchive(unreachable) }, 'Choose folder')))
}

// chooseArchive(old, show) asks for the archive folder, a detected sync folder too, and
// confirms a change with the old and new folders (Ruling P62). old is null for a first
// folder (nothing to confirm), '' when it is not known; it may be a promise of either.
// show formats a path for the dialog. It gives the new path, or null.
export async function chooseArchive(old, show = (p) => p) {
  const path = await chooseFolder({ title: 'Choose the archive folder' }) // the detected sync folders first (Ruling P52)
  if (!path || old == null) return path
  const was = await old
  if (path === was) return path
  const ok = await confirmDialog({ title: `Change the archive folder to ${show(path)}?`,
    body: `Your other computers keep using ${was ? show(was) : 'the old folder'} until you change them too.`, confirm: 'Change folder' })
  return ok ? path : null
}

async function setArchive(unreachable) {
  // The folder that can't be reached is the old one; read while the chooser is open.
  const old = unreachable ? api.get('/api/settings').then((s) => (s && s.config && s.config.inUse) || '', () => '') : null
  const path = await chooseArchive(old)
  if (!path) return
  try {
    const st = await api.run('config', { action: 'set-destination', args: [path] }, { wait: true })
    if (st.ok) location.reload()
    else toast(st.error || GENERIC)
  } catch (e) {
    toast(e.plain ? e.message : GENERIC)
  }
}

// --- Router ------------------------------------------------------------------------

const screens = {}
let current = null, shown = '', navN = 0, mounted = null

// register(name, {mount(el, params, signal), unmount()}). mount fills the cloned template
// in el and may return a promise of its first view: until it settles the screen shows
// only its h1 and a loading line. A view with `problem` set shows the shared empty
// state instead (a screen that places noArchive() itself, like Settings, returns
// nothing); a rejection shows its plain message with Try again. params are the hash's
// query (#/conversations?page=2); change it with history.replaceState to keep the
// screen mounted, or with location.hash to remount it. signal is aborted when the
// screen goes: pass it to follow() so no event stream outlives its screen.
export function register(name, screen) {
  screens[name] = screen
}

async function show(force, first) {
  const hash = location.hash
  if (!hash.startsWith('#/') || (hash === shown && !force)) return // #main and other in-page links (Ruling P26)
  const [name, q] = hash.slice(2).split('?')
  if (!SCREENS.includes(name)) return location.replace('#/overview')
  shown = hash
  const n = ++navN
  for (const cancel of [...openDialogs].reverse()) cancel()
  if (mounted) mounted.abort()
  mounted = null
  if (current && current.unmount) try { current.unmount() } catch (e) { console.error(e) }
  current = null
  // A module that fails to load (often the panel stopping) shows the error, not a bare template.
  if (!screens[name]) await import(`./${name}.js`).catch((e) => { console.error(e); beat() })
  if (n !== navN) return

  main.replaceChildren(document.getElementById('t-' + name).content.cloneNode(true))
  for (const a of nav.querySelectorAll('.nav a')) {
    if (a.getAttribute('href') === '#/' + (SECTION[name] || name)) a.setAttribute('aria-current', 'page')
    else a.removeAttribute('aria-current')
  }
  const h1 = main.querySelector('h1')
  document.title = h1.textContent + ' - claude-sessions'
  if (!first) { scrollTo(0, 0); h1.focus() }

  const s = current = screens[name] || { mount() { throw fail(GENERIC) } }
  const unhide = isolate(h1)
  // Until the first view: a skeleton of rows, and "Loading…" for screen readers.
  const said = h('span', { class: 'sr' })
  const line = h('div', { class: 'skel-view stack gap-16', role: 'status' }, said,
    h('span', { class: 'skel tall w-30', 'aria-hidden': 'true' }),
    h('ul', { class: 'panel rows skel-rows', 'aria-hidden': 'true' }, [75, 60, 45, 60].map((w) => h('li', null, h('span', { class: 'skel w-' + w })))))
  let top = h1
  while (top.parentElement !== main) top = top.parentElement
  top.after(line)
  rise([top])
  setTimeout(() => { said.textContent = 'Loading…' }) // filled after it is in the page, so it is announced
  const retry = () => show(true)
  mounted = new AbortController()
  const instead = (el) => { line.replaceWith(el); rise([el]) }
  try {
    const view = await s.mount(main, new URLSearchParams(q || ''), mounted.signal)
    if (n !== navN) return
    if (view && view.problem) instead(noArchive(view.problem, retry))
    else { line.remove(); rise(unhide()); goTo(name, q) }
  } catch (e) {
    if (n !== navN) return
    if (!e || !e.plain) console.error(e)
    instead(h('div', { class: 'panel pad stack', role: 'alert' }, h('p', null, e && e.plain ? e.message : GENERIC),
      h('div', { class: 'row' }, h('button', { type: 'button', class: 'btn', onclick: retry }, 'Try again'))))
  }
}

// goTo follows a link to a place in a screen (#/settings?at=log-h, Overview's "Open the
// full log"): that heading comes into view with the focus, and the address loses the
// at, so a reload opens the screen at its top. Never animated: it is where the link goes.
function goTo(name, q) {
  const params = new URLSearchParams(q || ''), id = params.get('at')
  const to = id && main.querySelector('#' + CSS.escape(id))
  if (!to) return
  params.delete('at')
  const rest = params.toString()
  shown = '#/' + name + (rest ? '?' + rest : '')
  history.replaceState(null, '', shown)
  to.tabIndex = -1
  to.focus({ preventScroll: true })
  to.scrollIntoView({ block: 'start' })
}

// rise brings a screen's blocks into view as it opens: a 6 px rise and a fade, each 40 ms
// after the one before, the fifth and later together. Only on opening, never on updates.
function rise(els) {
  if (reducedMotion) return
  els.filter((el) => el.getClientRects().length).forEach((el, i) => el.animate(
    [{ opacity: 0, transform: 'translateY(6px)' }, { opacity: 1, transform: 'none' }],
    { duration: 200, easing: 'cubic-bezier(.23,1,.32,1)', delay: Math.min(i, 4) * 40, fill: 'backwards' }))
}

// isolate hides everything in main but the h1 (and Back links), through style so it
// never fights a screen's own `hidden`; it gives the function that shows them again,
// which gives them.
function isolate(h1) {
  const hid = []
  for (let n = h1; n !== main; n = n.parentElement) {
    for (const s of n.parentElement.children) {
      if (s !== n && !s.classList.contains('back') && s.style.display !== 'none') { s.style.display = 'none'; hid.push(s) }
    }
  }
  return () => { for (const s of hid) s.style.display = ''; return hid }
}

// Tables marked .cards are one card per row on a phone (panel.css), each cell labelled by
// its column: the header's words go into every cell's data-label, as rows come and go. A
// header that is only for screen readers (an Actions column) labels nothing.
new MutationObserver(() => {
  for (const t of main.querySelectorAll('table.cards')) {
    const names = [...t.querySelectorAll('thead th')].map((th) => (th.querySelector('.sr') ? '' : th.textContent.trim()))
    for (const tr of t.querySelectorAll('tbody tr')) {
      [...tr.cells].forEach((td, i) => { if (names[i] && td.colSpan === 1) td.dataset.label = names[i] })
    }
  }
}).observe(main, { childList: true, subtree: true })

// --- Heartbeat, stopped notice, rail foot ------------------------------------------

let dead = false, leaving = false, beatTimer, pollTimer, busy = null
let footText = '', summaryAt = 0 // the last summary's foot text, and when a summary was last asked for
addEventListener('pagehide', () => { leaving = true })
addEventListener('pageshow', () => { leaving = false })

// stopped covers the page with the notice once a request finds no server.
function stopped() {
  if (dead || leaving) return
  dead = true
  clearInterval(beatTimer)
  clearTimeout(pollTimer)
  for (const el of document.body.children) el.inert = true
  const note = h('div', { class: 'stopped', role: 'alert', tabindex: '-1' }, h('h1', null, 'The panel has stopped.'),
    h('p', null, 'Run ', h('code', null, 'claude-sessions ui'), ' again, or open the claude-sessions panel shortcut.'))
  document.body.append(note)
  note.focus()
}

// beat gives false once the panel is gone: a network error, or a session it no
// longer knows (403).
function beat() {
  return post('/api/heartbeat').then(() => true, (e) => {
    if (!e.status || e.status === 403) { stopped(); return false }
    return true
  })
}

// poll shows the running job in the rail foot, every 5 s while one runs, and the last
// backup time otherwise.
async function poll() {
  clearTimeout(pollTimer)
  if (dead) return
  const asked = Date.now()
  let st
  try { st = await get('/api/state') } catch { return }
  if (!st) return
  nav.querySelector('[data-slot="machine"]').textContent = st.machine || ''
  const job = st.job && st.job.running ? st.job : null
  const foot = nav.querySelector('[data-slot="foot"]')
  nav.querySelector('[data-slot="dot"]').classList.toggle('live', !!job)
  // A job that had started when this poll asked, and is not running now, has ended.
  for (const [id, j] of jobs) if (j.at <= asked && (!job || job.id !== id)) ended(id)
  strip.hidden = !job
  if (job) {
    foot.textContent = strip.firstElementChild.textContent = 'Running: ' + job.label // a state, not the button's words
    pollTimer = setTimeout(poll, 5000)
  } else if (busy !== false) {
    // The first poll, or a job just ended: the last backup again, read afresh unless a
    // screen has asked for the summary since this poll began.
    if (footText) foot.textContent = footText
    if (summaryAt < asked) get('/api/summary').catch(() => { /* the foot keeps its text */ })
  }
  busy = !!job
}

// lastBackup keeps a summary's last backup as the rail foot's text, shown unless a job
// is running; poll shows it once the job ends.
function lastBackup(summary) {
  const t = when(summary && summary.lastBackup)
  footText = t ? 'Backed up ' + t.replace(/^Yesterday/, 'yesterday') : 'Not backed up yet'
  if (!busy) nav.querySelector('[data-slot="foot"]').textContent = footText
}

rail(nav, document.querySelector('.grip[role="separator"]'))
addEventListener('hashchange', () => show())
if (!location.hash.startsWith('#/')) history.replaceState(null, '', '#/overview')
show(false, true)
beat()
beatTimer = setInterval(beat, 30000)
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible' && !dead) { beat(); poll() } })
poll()
