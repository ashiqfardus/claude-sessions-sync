// The searchable dropdown (Combo.dc.html), as the WAI-ARIA combobox pattern with a
// listbox popup: typing filters, arrows move, Enter picks, Esc and blur close.
import { h, icon } from './app.js'

let comboN = 0

// combo(container, {id, label, options: [{value, label}], value, onChange}) fills the
// container. label defaults to its data-label. It gives {input, value, options}:
// value and options can be read and set; onChange(value) runs when the person picks
// a different option.
export function combo(container, { id, label, options = [], value, onChange } = {}) {
  id = id || 'combo-' + ++comboN
  label = label || container.dataset.label || ''
  let opts = options, sel = value === undefined && opts.length ? opts[0].value : value
  let open = false, q = '', active = 0, shown = []

  const input = h('input', { id, class: 'input combo-input', type: 'text', role: 'combobox', 'aria-expanded': 'false', 'aria-controls': id + '-list', 'aria-autocomplete': 'list', autocomplete: 'off', spellcheck: 'false' })
  const chev = icon(14, 'M6 9l6 6 6-6')
  chev.classList.add('combo-chev')
  const list = h('ul', { id: id + '-list', role: 'listbox', 'aria-label': label, class: 'combo-list pop', hidden: true })
  container.replaceChildren(h('div', { class: 'field combo' }, h('label', { for: id }, label), h('div', { class: 'combo-box' }, input, chev), list))

  const selected = () => opts.find((o) => o.value === sel)

  function render() {
    const s = selected(), text = s ? s.label : ''
    const v = open ? q : text
    if (input.value !== v) input.value = v // only when it differs, so the caret stays put
    input.placeholder = open ? 'Type to filter' : text
    input.setAttribute('aria-expanded', String(open))
    list.hidden = !open
    if (!open) return input.removeAttribute('aria-activedescendant')
    const f = q.toLowerCase()
    shown = f ? opts.filter((o) => o.label.toLowerCase().includes(f)) : opts
    list.replaceChildren(...shown.map((o, i) => h('li', { id: id + '-opt-' + i, role: 'option', class: 'combo-opt', 'aria-selected': String(o === s) },
      h('span', null, o.label), o === s && icon(14, 'M5 12.5l4.5 4.5L19 7.5'))))
    if (!shown.length) list.append(h('li', { role: 'presentation', class: 'combo-empty' }, `Nothing matches "${q}"`))
    mark(Math.min(active, shown.length - 1), true)
  }

  // mark moves the active option without rebuilding the list; keys also scroll it into view.
  function mark(i, scroll) {
    active = Math.max(i, 0)
    for (const li of list.querySelectorAll('.is-active')) li.classList.remove('is-active')
    const li = shown.length ? list.children[active] : null
    if (!li) return input.removeAttribute('aria-activedescendant')
    li.classList.add('is-active')
    input.setAttribute('aria-activedescendant', li.id)
    if (scroll) li.scrollIntoView({ block: 'nearest' })
  }

  function openList() {
    if (open) return
    open = true
    q = ''
    active = Math.max(opts.indexOf(selected()), 0)
    render()
  }
  function close() {
    open = false
    q = ''
    render()
  }
  function pick(o) {
    const changed = o.value !== sel
    sel = o.value
    close()
    if (changed && onChange) onChange(sel)
  }

  input.addEventListener('focus', openList)
  input.addEventListener('click', openList)
  input.addEventListener('blur', close)
  input.addEventListener('input', () => { q = input.value; open = true; active = 0; render() })
  input.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      if (!open) return openList()
      const n = shown.length
      if (n) mark((active + (e.key === 'ArrowDown' ? 1 : n - 1)) % n, true)
    } else if (e.key === 'Enter') {
      e.preventDefault() // never submits the filters form
      if (open && shown.length) pick(shown[active])
    } else if (e.key === 'Escape' && open) {
      e.preventDefault()
      e.stopPropagation() // closes the list, not the dialog around it
      close()
    }
  })
  // mousedown, not click: picking (or scrolling the list) must not take focus from the input.
  list.addEventListener('mousedown', (e) => {
    e.preventDefault()
    const li = e.target.closest('[role="option"]')
    if (li) pick(shown[[...list.children].indexOf(li)])
  })
  list.addEventListener('mousemove', (e) => {
    const li = e.target.closest('[role="option"]')
    const i = li ? [...list.children].indexOf(li) : -1
    if (i >= 0 && i !== active) mark(i)
  })
  render()

  return {
    input,
    get value() { return sel },
    set value(v) { sel = v; render() },
    get options() { return opts },
    set options(o) { opts = o; render() },
  }
}
