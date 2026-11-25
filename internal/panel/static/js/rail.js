// The resizable sidebar (Main.dc.html; Ruling P11): the separator drags between MIN
// and MAX, folds to icons below SNAP, and the width is remembered. Below 760 px the
// stylesheet turns the rail into a top bar and hides the separator.
const RAIL = 200, MIN = 64, MAX = 320, SNAP = 140, STEP = 16
const KEY = 'cs.railWidth'
const clamp = (w) => Math.max(MIN, Math.min(MAX, Math.round(w)))
const snap = (w) => (w < SNAP ? MIN : w)

export function rail(nav, grip) {
  let w = RAIL, drag = null
  try { w = snap(clamp(Number(localStorage.getItem(KEY)) || RAIL)) } catch { /* storage off: the default */ }

  const set = (n, save) => {
    w = n
    nav.style.width = w + 'px'
    nav.classList.toggle('narrow', w < SNAP)
    grip.setAttribute('aria-valuenow', w)
    if (save) try { localStorage.setItem(KEY, String(w)) } catch { /* not remembered */ }
  }
  const toggle = () => set(w < SNAP ? RAIL : MIN, true)
  set(w)

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
}
