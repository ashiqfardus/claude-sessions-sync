// The folder browser: a dialog over GET /api/folders, which lists folder names only.
import { h, api, modal } from './app.js'

// chooseFolder({title, start}) gives the chosen folder's path, or null. The detected
// sync folders choose at once; the roots, the typed path, Up and the subfolders
// browse. The server checks every path; its plain error shows in the dialog.
export function chooseFolder({ title, start } = {}) {
  return modal(title || 'Choose a folder', null, (box, done) => {
    const fid = box.getAttribute('aria-labelledby') + '-path'
    const path = h('input', { id: fid, class: 'input mono', type: 'text', autocomplete: 'off', spellcheck: 'false' })
    const hidden = h('input', { type: 'checkbox', class: 'check' })
    const up = h('button', { type: 'button', class: 'btn sm', disabled: true }, 'Up')
    const detected = h('div', { class: 'stack gap-6', hidden: true })
    const roots = h('div', { class: 'row gap-6' })
    const error = h('p', { class: 'note red', role: 'alert', hidden: true })
    const status = h('p', { class: 'muted small', hidden: true })
    const list = h('ul', { class: 'folder-browser', 'aria-label': 'Folders inside' })
    let parent = '', current = start || '', n = 0

    async function go(to) {
      const mine = ++n
      status.textContent = 'Loading…'
      status.hidden = false
      let v
      try {
        v = await api.get('/api/folders', { path: to || undefined, hidden: hidden.checked ? 1 : undefined })
      } catch (e) {
        if (mine !== n) return
        status.hidden = true
        error.textContent = e.message // api errors are plain sentences
        error.hidden = false
        return
      }
      if (mine !== n) return
      current = path.value = v.path || ''
      parent = v.parent || ''
      up.disabled = !parent
      error.textContent = v.error || ''
      error.hidden = !v.error
      status.textContent = v.truncated ? 'Showing the first 2,000 folders.' : ''
      status.hidden = !v.truncated

      const found = v.detected || []
      detected.hidden = !found.length
      detected.replaceChildren(h('p', { class: 'lbl' }, 'Sync folders on this computer'),
        h('div', { class: 'row gap-6' }, found.map((f) => h('button', { type: 'button', class: 'btn sm', title: f.path, onclick: () => done(f.path) }, f.name))))
      roots.replaceChildren(...(v.roots || []).map((r) => h('button', { type: 'button', class: 'btn sm', title: r.path, onclick: () => go(r.path) }, r.name)))
      const entries = v.entries || []
      list.replaceChildren(...entries.map((f) => h('li', null, h('button', { type: 'button', class: 'btn', onclick: () => go(f.path) }, f.name))))
      if (!entries.length && !v.error) list.append(h('li', { class: 'muted small combo-empty' }, 'No folders inside.'))
      list.hidden = !list.children.length
      // The subfolder or root button that had focus was just replaced: move focus to the new list.
      if (!box.contains(document.activeElement)) (list.querySelector('button') || (up.disabled ? path : up)).focus()
    }

    up.addEventListener('click', () => go(parent))
    hidden.addEventListener('change', () => go(current))
    path.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); go(path.value.trim()) } })
    const choose = () => { const p = path.value.trim(); if (p) done(p) }

    box.append(detected, roots,
      h('div', { class: 'row fields' },
        h('div', { class: 'field grow' }, h('label', { for: fid }, 'Folder'), path),
        h('button', { type: 'button', class: 'btn', onclick: () => go(path.value.trim()) }, 'Go to')),
      error,
      h('div', { class: 'row spread' }, up, h('label', { class: 'opt' }, hidden, 'Show hidden folders')),
      list, status,
      h('div', { class: 'row end' },
        h('button', { type: 'button', class: 'btn', onclick: () => done(null) }, 'Cancel'),
        h('button', { type: 'button', class: 'btn primary', onclick: choose }, 'Choose this folder')))
    go(current)
    return path
  })
}
