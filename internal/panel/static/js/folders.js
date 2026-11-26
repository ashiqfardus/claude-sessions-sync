// The folder browser: a dialog over GET /api/folders, which lists folder names only.
import { h, api, modal, GENERIC } from './app.js'

// full is a typed path made absolute, ~ being the home folder, or '' when it is not a
// full path (Ruling P59): a relative one would resolve against wherever the panel started.
function full(p, home) {
  const win = home.includes('\\')
  if (home && (p === '~' || /^~[\\/]/.test(p))) p = home + p.slice(1)
  return (win ? /^[A-Za-z]:[\\/]/ : /^\//).test(p) ? p : ''
}

// chooseFolder({title, start}) gives the chosen folder's full path, or null. The detected
// sync folders choose at once; the roots, the typed path, Up and the subfolders
// browse. A typed path that is not a full one is refused here; the server checks the
// rest, and either error shows in the dialog.
export function chooseFolder({ title, start } = {}) {
  return modal(title || 'Choose a folder', null, (box, done) => {
    const fid = box.getAttribute('aria-labelledby') + '-path'
    const path = h('input', { id: fid, class: 'input mono', type: 'text', autocomplete: 'off', spellcheck: 'false', 'aria-describedby': fid + '-hint' })
    const hint = h('p', { id: fid + '-hint', class: 'muted small' }, 'Type a full path, such as ~/Documents.')
    const hidden = h('input', { type: 'checkbox', class: 'check' })
    const up = h('button', { type: 'button', class: 'btn sm', disabled: true }, 'Up')
    const detected = h('div', { class: 'stack gap-6', hidden: true })
    const roots = h('div', { class: 'row gap-6' })
    const error = h('p', { class: 'note red', role: 'alert', hidden: true })
    const status = h('span', { class: 'muted small', role: 'status' }) // loading and truncation, announced
    const list = h('ul', { class: 'folder-browser', 'aria-label': 'Folders inside' })
    let parent = '', current = start || '', n = 0, home = ''
    // typed checks the path in the field; a path that is not a full one shows why, here.
    function typed() {
      const p = full(path.value.trim(), home)
      if (!p) {
        error.textContent = "That isn't a full path. Start it with ~ or " + (home.includes('\\') ? 'a drive letter, such as D:\\.' : '/.')
        error.hidden = false
        path.focus()
      }
      return p
    }

    async function go(to) {
      const mine = ++n, before = path.value // what the field held when this listing was asked for
      status.textContent = 'Loading…'
      let v
      try {
        v = await api.get('/api/folders', { path: to || undefined, hidden: hidden.checked ? 1 : undefined })
        if (!v || typeof v !== 'object') throw new Error('not a folder list')
      } catch (e) {
        if (mine !== n) return
        status.textContent = ''
        error.textContent = e.plain ? e.message : GENERIC
        error.hidden = false
        return
      }
      if (mine !== n) return
      current = v.path || ''
      if (path.value === before) path.value = current // never over a path typed while it loaded
      home = (v.roots && v.roots[0] && v.roots[0].path) || home // Roots puts the home folder first
      if (home.includes('\\')) hint.textContent = 'Type a full path, such as ~\\Documents or D:\\Backups.'
      parent = v.parent || ''
      up.disabled = !parent
      error.textContent = v.error || ''
      error.hidden = !v.error
      status.textContent = v.truncated ? 'Showing the first 2,000 folders.' : ''

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
    const goTyped = () => { const p = typed(); if (p) go(p) }
    path.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); goTyped() } })
    const choose = () => { const p = typed(); if (p) done(p) }

    box.append(detected, roots,
      h('div', { class: 'row fields' },
        h('div', { class: 'field grow' }, h('label', { for: fid }, 'Folder'), path),
        h('button', { type: 'button', class: 'btn', onclick: goTyped }, 'Go to')),
      hint, error,
      h('div', { class: 'row spread' }, up, status, h('label', { class: 'opt' }, hidden, 'Show hidden folders')),
      list,
      h('div', { class: 'row end' },
        h('button', { type: 'button', class: 'btn', onclick: () => done(null) }, 'Cancel'),
        h('button', { type: 'button', class: 'btn primary', onclick: choose }, 'Choose this folder')))
    go(current)
    return path
  })
}
