// The page buttons (Conversations.dc.html): ‹ 1 2 3 4 … 20 21 ›.
import { h, icon } from './app.js'

// pageNumbers(page, pages) gives the numbers to show, with 0 for each "…" gap: 1, the
// last two, the current page and its neighbours, and 2-4 or the last 4 near either end.
export function pageNumbers(c, last) {
  const want = new Set([1, last - 1, last, c - 1, c, c + 1])
  if (c <= 3) [2, 3, 4].forEach((n) => want.add(n))
  if (c >= last - 2) [last - 3, last - 2].forEach((n) => want.add(n))
  const nums = [...want].filter((n) => n >= 1 && n <= last).sort((a, b) => a - b)
  return nums.flatMap((n, i) => (i && n - nums[i - 1] > 1 ? [0, n] : [n]))
}

// pager(el, {page, pages, onGo}) fills el (the .pager span) and calls onGo(n). It can
// be called again with the new page; focus stays on the same button.
export function pager(el, { page, pages, onGo }) {
  const had = el.contains(document.activeElement) && document.activeElement.getAttribute('aria-label')
  const btn = (label, n, kids, disabled) => h('button', { type: 'button', class: 'btn sm', 'aria-label': label, disabled, onclick: () => onGo(n) }, kids)
  el.replaceChildren()
  if (pages < 1) return
  el.append(btn('Previous page', page - 1, icon(14, 'M15 6l-6 6 6 6'), page <= 1),
    ...pageNumbers(page, pages).map((n) => {
      if (!n) return h('span', { class: 'gap', 'aria-hidden': 'true' }, '…')
      const b = btn('Page ' + n, n, n)
      if (n === page) b.setAttribute('aria-current', 'page')
      return b
    }),
    btn('Next page', page + 1, icon(14, 'M9 6l6 6-6 6'), page >= pages))
  if (had) (el.querySelector(`[aria-label="${had}"]:not(:disabled)`) || el.querySelector('[aria-current="page"]')).focus()
}
