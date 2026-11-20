package render

import "html"

// The CSS is inline rather than a separate file, and there is no JavaScript.
//
// These pages are read from a cloud folder on a phone, often through a sync client's
// own viewer, where a relative stylesheet may not resolve and scripts may not run. One
// self-contained file always works.
const style = `
:root {
  color-scheme: light dark;
  --bg: #ffffff; --fg: #1a1a1a; --muted: #6b6b6b; --line: #e4e4e7;
  --user: #f4f6f8; --assistant: #ffffff; --code: #f6f8fa; --accent: #3b5bdb;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #16181c; --fg: #e6e6e6; --muted: #9a9a9a; --line: #2a2d34;
    --user: #1e2128; --assistant: #16181c; --code: #1b1e24; --accent: #8aa2ff;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0; padding: 1rem;
  background: var(--bg); color: var(--fg);
  font: 16px/1.55 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  overflow-wrap: anywhere;
}
main { max-width: 46rem; margin: 0 auto; }
h1 { font-size: 1.15rem; margin: 0 0 .25rem; }
h2 { font-size: 1rem; margin: 1.5rem 0 .5rem; color: var(--muted); font-weight: 600; }
a { color: var(--accent); }
.meta, .back { color: var(--muted); font-size: .82rem; margin: 0 0 1rem; }

.turn { border: 1px solid var(--line); border-radius: 10px; padding: .7rem .8rem; margin: .6rem 0; }
.turn.user { background: var(--user); }
.turn.assistant { background: var(--assistant); }
.who { font-size: .72rem; text-transform: uppercase; letter-spacing: .06em; color: var(--muted); margin-bottom: .4rem; }
.time { float: right; text-transform: none; letter-spacing: 0; }
.t { white-space: pre-wrap; }

pre { background: var(--code); border: 1px solid var(--line); border-radius: 8px;
      padding: .6rem; overflow-x: auto; font-size: .82rem; }
code { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.t code { background: var(--code); border: 1px solid var(--line); border-radius: 4px;
          padding: 0 .25em; font-size: .88em; }
.t strong.h { display: inline-block; font-size: 1.05em; margin-top: .35em; }
/* Wide tables scroll inside their box, never the page. overflow-wrap is reset so
   cells keep whole words instead of collapsing to a letter per line on phones. */
.tw { overflow-x: auto; margin: .35rem 0; }
.tw table { border-collapse: collapse; font-size: .9rem; white-space: normal; overflow-wrap: normal; }
.tw th, .tw td { border: 1px solid var(--line); padding: .3rem .55rem; text-align: left; vertical-align: top; }
.tw th { background: var(--code); font-weight: 600; }

details { margin: .45rem 0; border-left: 2px solid var(--line); padding-left: .6rem; }
summary { cursor: pointer; color: var(--muted); font-size: .82rem; }
details.tool > summary { color: var(--accent); }
.trunc, .skipped { color: var(--muted); font-size: .78rem; font-style: italic; margin-top: .4rem; }

ul.sessions { list-style: none; padding: 0; margin: 0; }
ul.sessions li { border-bottom: 1px solid var(--line); }
ul.sessions a { display: block; padding: .6rem .2rem; text-decoration: none; color: inherit; }
ul.sessions a:hover { background: var(--user); }
.when { display: block; font-size: .74rem; color: var(--muted); }
.prompt { display: block; }
.size { display: block; font-size: .72rem; color: var(--muted); }
`

// csp forbids every script and every remote load. Pages carry text from transcripts
// and from paths other machines recorded; escaping is the first defence, and this
// makes a missed escape - or a javascript: link - inert anyway.
const csp = `default-src 'none'; style-src 'unsafe-inline'`

func pageHead(title string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta http-equiv="Content-Security-Policy" content="` + csp + `">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<title>` + html.EscapeString(title) + `</title><style>` + style + `</style>` +
		`</head><body><main>`
}

func pageFoot() string {
	return `<p class="meta">Rendered by claude-sessions. The original .jsonl files are unmodified.</p>` +
		`</main></body></html>`
}
