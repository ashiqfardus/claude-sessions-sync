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
const SECTION = { delete: 'storage', import: 'overview', commands: 'settings' }

const token = document.querySelector('meta[name="cs-token"]').content
const app = document.querySelector('.app')
const main = document.getElementById('main')
const nav = document.getElementById('rail')
const dialogs = document.getElementById('dialogs')

// README is the panel's one external link (the About link in Settings).
export const README = 'https://github.com/ashiqfardus/claude-sessions-sync#readme'
let readmeLinked = false

// Attributes that load or link elsewhere, refused outright (any name ending in src but
// src itself is refused too). href and src must resolve to this panel.
const REFUSED = ['style', 'srcdoc', 'ping', 'data', 'poster', 'background', 'manifest', 'codebase', 'archive', 'formaction', 'action', 'srcset', 'xlink:href']
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
  for (const [k, v] of Object.entries(attrs || {})) {
    const n = k.toLowerCase()
    if (v == null || v === false) continue
    if (REFUSED.includes(n) || (n.endsWith('src') && n !== 'src')) throw new Error('h: refused ' + n)
    if (n.startsWith('on')) {
      if (typeof v !== 'function') throw new Error('h: refused ' + n)
      el.addEventListener(n.slice(2), v)
    } else if ((n === 'href' || n === 'src') && !local(v) && !(n === 'href' && readmeLinked && v === README)) {
      throw new Error('h: refused ' + n)
    } else el.setAttribute(n, v === true ? '' : v)
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

export const api = {
  // get('/api/summary', {page: 2}) gives the decoded JSON. Two callers asking for the
  // same URL at once share one request; every summary also updates the rail foot.
  get(path, params) {
    const url = path + query(params)
    if (!inflight.has(url)) {
      inflight.set(url, call(url).then((v) => { if (path === '/api/summary') lastBackup(v); return v })
        .finally(() => inflight.delete(url)))
    }
    return inflight.get(url)
  },
  // run('push', {dryRun: true}) starts an action and gives {id}; with {wait: true} it
  // gives the job's final State. A running job answers 409 with a plain error.
  run: (name, body, { wait } = {}) => {
    if (wait) setTimeout(poll, 500) // the request is held until the job ends; show it in the rail meanwhile
    return post('/api/run/' + encodeURIComponent(name) + (wait ? '?wait=1' : ''), body).finally(() => { poll() })
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
      poll()
      const d = parse(e.data)
      if (d && typeof d === 'object') resolve({ id, ...d, lines })
      else reject(fail(GENERIC))
    })
    es.onerror = () => {
      // CLOSED: the server refused the stream (an unknown job). Otherwise the browser is
      // reconnecting; a heartbeat tells a dropped stream from a stopped panel.
      if (es.readyState === EventSource.CLOSED) { end(); return reject(fail(GENERIC, 404)) }
      beat().then((alive) => { if (!alive) { es.close(); end(); reject(fail(GENERIC, 0)) } })
    }
  })
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
    el.remove()
  }
  const arm = () => { if (el.isConnected) { clearTimeout(timer); timer = setTimeout(close, 4500) } }
  el.addEventListener('mouseenter', () => clearTimeout(timer))
  el.addEventListener('mouseleave', arm)
  el.addEventListener('focusin', () => clearTimeout(timer))
  el.addEventListener('focusout', arm)
  document.getElementById('toasts').append(el)
  arm()
}

// --- Dialogs -----------------------------------------------------------------------

let dialogN = 0
const openDialogs = new Set() // each open dialog's cancel, so navigation can close them

// modal(title, cancelValue, build) opens a modal dialog. build(box, done) adds the
// content and returns the element to focus first; done(value) closes it. Esc gives
// cancelValue. Focus stays inside and goes back to the opener at the end.
export function modal(title, cancelValue, build) {
  return new Promise((resolve) => {
    const opener = document.activeElement
    const id = 'dlg-' + ++dialogN
    const box = h('div', { class: 'dialog', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': id }, h('h2', { id }, title))
    const scrim = h('div', { class: 'scrim' }, box)
    const onKey = (e) => {
      if (dialogs.lastElementChild !== scrim) return // a dialog opened from this one
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
      scrim.remove()
      if (!dead) (dialogs.lastElementChild || app).inert = false
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
// "none" or "unreachable". Choose folder runs config set-destination, then reloads.
export function noArchive(problem, retry) {
  const unreachable = problem === 'unreachable'
  return h('div', { class: 'panel pad stack' }, h('p', null, unreachable ? UNREACHABLE : NO_ARCHIVE),
    h('div', { class: 'row' },
      h('button', { type: 'button', class: 'btn primary', onclick: setArchive }, 'Choose folder'),
      unreachable && retry && h('button', { type: 'button', class: 'btn', onclick: retry }, 'Try again')))
}

async function setArchive() {
  const path = await chooseFolder({ title: 'Choose the archive folder' })
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
  if (!screens[name]) await import(`./${name}.js`).catch((e) => console.error(e))
  if (n !== navN) return

  main.replaceChildren(document.getElementById('t-' + name).content.cloneNode(true))
  for (const a of nav.querySelectorAll('.nav a')) {
    if (a.getAttribute('href') === '#/' + (SECTION[name] || name)) a.setAttribute('aria-current', 'page')
    else a.removeAttribute('aria-current')
  }
  const h1 = main.querySelector('h1')
  document.title = h1.textContent + ' - claude-sessions'
  if (!first) { scrollTo(0, 0); h1.focus() }

  const s = current = screens[name] || null
  if (!s) return
  const unhide = isolate(h1)
  const line = h('p', { class: 'muted', role: 'status' })
  let top = h1
  while (top.parentElement !== main) top = top.parentElement
  top.after(line)
  setTimeout(() => { line.textContent = 'Loading…' }) // filled after it is in the page, so it is announced
  const retry = () => show(true)
  mounted = new AbortController()
  try {
    const view = await s.mount(main, new URLSearchParams(q || ''), mounted.signal)
    if (n !== navN) return
    if (view && view.problem) line.replaceWith(noArchive(view.problem, retry))
    else { line.remove(); unhide() }
  } catch (e) {
    if (n !== navN) return
    if (!e || !e.plain) console.error(e)
    line.replaceWith(h('div', { class: 'panel pad stack', role: 'alert' }, h('p', null, e && e.plain ? e.message : GENERIC),
      h('div', { class: 'row' }, h('button', { type: 'button', class: 'btn', onclick: retry }, 'Try again'))))
  }
}

// isolate hides everything in main but the h1 (and Back links), through style so it
// never fights a screen's own `hidden`; it gives the function that shows them again.
function isolate(h1) {
  const hid = []
  for (let n = h1; n !== main; n = n.parentElement) {
    for (const s of n.parentElement.children) {
      if (s !== n && !s.classList.contains('back') && s.style.display !== 'none') { s.style.display = 'none'; hid.push(s) }
    }
  }
  return () => { for (const s of hid) s.style.display = '' }
}

// --- Heartbeat, stopped notice, rail foot ------------------------------------------

let dead = false, leaving = false, beatTimer, pollTimer, busy = null
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
  let st
  try { st = await api.get('/api/state') } catch { return }
  if (!st) return
  nav.querySelector('[data-slot="machine"]').textContent = st.machine || ''
  const job = st.job && st.job.running ? st.job : null
  nav.querySelector('[data-slot="dot"]').classList.toggle('live', !!job)
  if (job) {
    nav.querySelector('[data-slot="foot"]').textContent = job.label
    pollTimer = setTimeout(poll, 5000)
  } else if (busy !== false && Date.now() - summaryAt > 3000) {
    // The first poll, or a job just ended, and no screen has just read the summary.
    api.get('/api/summary').catch(() => { /* the foot keeps its text */ })
  }
  busy = !!job
}

let summaryAt = 0

// lastBackup shows a summary's last backup in the rail foot, unless a job is running.
function lastBackup(summary) {
  summaryAt = Date.now()
  const t = when(summary && summary.lastBackup)
  if (!busy) nav.querySelector('[data-slot="foot"]').textContent = t ? 'Backed up ' + t.replace(/^Yesterday/, 'yesterday') : 'Not backed up yet'
}

rail(nav, document.querySelector('.grip[role="separator"]'))
addEventListener('hashchange', () => show())
if (!location.hash.startsWith('#/')) history.replaceState(null, '', '#/overview')
show(false, true)
beat()
beatTimer = setInterval(beat, 30000)
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible' && !dead) { beat(); poll() } })
poll()
