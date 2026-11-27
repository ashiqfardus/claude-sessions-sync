// The resizable sidebar (Main.dc.html; Ruling P11): the separator drags between MIN
// and MAX, folds to icons below SNAP, and the width is remembered. From 761 to 1023 px
// it starts folded to icons. Up to 760 px the stylesheet turns the rail into a top bar,
// hides the separator, and its Menu button opens the links and the rail foot beneath it.
const RAIL = 200, MIN = 64, MAX = 320, SNAP = 140, STEP = 16
const KEY = 'cs.railWidth'
const clamp = (w) => Math.max(MIN, Math.min(MAX, Math.round(w)))
const snap = (w) => (w < SNAP ? MIN : w)
const phone = matchMedia('(max-width: 760px)') // the stylesheet's top-bar breakpoint
const tablet = matchMedia('(min-width: 761px) and (max-width: 1023px)')

function saved() {
  try { return snap(clamp(Number(localStorage.getItem(KEY)) || RAIL)) } catch { return RAIL } // storage off: the default
}

export function rail(nav, grip) {
  let w = tablet.matches ? MIN : saved(), drag = null

  const set = (n, save) => {
    w = n
    nav.style.width = w + 'px'
    nav.classList.toggle('narrow', w < SNAP && !phone.matches) // the top bar is never folded
    grip.setAttribute('aria-valuenow', w)
    if (save) try { localStorage.setItem(KEY, String(w)) } catch { /* not remembered */ }
  }
  const toggle = () => set(w < SNAP ? RAIL : MIN, true)
  set(w)
  phone.addEventListener('change', () => { open(false); set(w) })
  tablet.addEventListener('change', () => set(tablet.matches ? MIN : saved()))

  grip.addEventListener('pointerdown', (e) => {
    if (e.button !== 0) return
    grip.setPointerCapture(e.pointerId)
    drag = { x: e.clientX, w }
    nav.classList.add('dragging')
    grip.classList.add('on')
  })
  grip.addEventListener('pointermove', (e) => { if (drag) set(clamp(drag.w + e.clientX - drag.x)) })
  const end = () => {
    if (!drag) return
    drag = null
    nav.classList.remove('dragging')
    grip.classList.remove('on')
    set(snap(w), true)
  }
  grip.addEventListener('pointerup', end)
  grip.addEventListener('pointercancel', end)
  grip.addEventListener('lostpointercapture', end)

  grip.addEventListener('keydown', (e) => {
    const step = { ArrowLeft: -STEP, ArrowRight: STEP }[e.key]
    if (step) {
      e.preventDefault()
      let n = clamp(w + step)
      // Inside the fold zone a step folds, or unfolds to SNAP, so ArrowRight can open a folded rail.
      if (n < SNAP) n = step < 0 ? MIN : SNAP
      set(n, true)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      toggle()
    }
  })
  grip.addEventListener('dblclick', toggle)

  // The phone's menu: a disclosure. A link, Escape, or a click outside closes it.
  const menu = nav.querySelector('[data-slot="menu"]')
  function open(on) {
    nav.classList.toggle('open', on)
    menu.setAttribute('aria-expanded', String(on))
  }
  menu.addEventListener('click', () => open(!nav.classList.contains('open')))
  nav.addEventListener('click', (e) => { if (e.target.closest('a')) open(false) })
  document.addEventListener('click', (e) => { if (!nav.contains(e.target)) open(false) })
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && nav.classList.contains('open')) { open(false); menu.focus() }
  })
}
