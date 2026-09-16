package render

import "regexp"

// Pages are opened from a synced folder - in a phone app, a browser, a Drive preview -
// and a session routinely contains secrets: `cat .env`, an `export API_KEY=...` in a
// Bash call, a token pasted into a prompt. The .jsonl keeps everything; the page
// masks what is recognisably a credential.
//
// ponytail: pattern-based, so it catches common key formats and NAME=value pairs, not
// every secret. A project that must not be readable at all belongs in renderExclude.

const redacted = "[REDACTED]"

// secretTokens are whole tokens with a recognisable shape.
var secretTokens = regexp.MustCompile(anyOf(
	`sk-ant-[A-Za-z0-9_\-]{20,}`,                // Anthropic
	`sk-(?:proj-|svcacct-)?[A-Za-z0-9_\-]{20,}`, // OpenAI
	`gh[pousr]_[A-Za-z0-9]{30,}`,                // GitHub tokens
	`github_pat_[A-Za-z0-9_]{40,}`,
	`glpat-[A-Za-z0-9_\-]{20,}`,                                           // GitLab
	`xox[abposr]-[A-Za-z0-9\-]{10,}`,                                      // Slack
	`AKIA[0-9A-Z]{16}`,                                                    // AWS access key id
	`AIza[0-9A-Za-z_\-]{35}`,                                              // Google API key
	`(?:sk|rk)_live_[0-9A-Za-z]{20,}`,                                     // Stripe
	`eyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`, // JWT
	`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`,
))

// secretAssignments keep the name and mask the value: DB_PASSWORD=..., "apiKey": "...",
// Authorization: Bearer ...
var secretAssignments = regexp.MustCompile(
	`(?i)([A-Za-z0-9_.\-]*(?:secret|token|passw(?:or)?d|pwd|api[_\-]?key|access[_\-]?key|private[_\-]?key|client[_\-]?secret|auth)[A-Za-z0-9_.\-]*["']?\s*[:=]\s*["']?(?:bearer\s+|basic\s+)?)([^\s"'<>,;]{6,})`)

// urlPasswords masks the password in scheme://user:password@host.
var urlPasswords = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://[^\s:/@]+:)([^\s@/]+)(@)`)

// Redact masks credentials in text destined for a rendered page.
func Redact(text string) string {
	text = secretTokens.ReplaceAllString(text, redacted)
	text = secretAssignments.ReplaceAllString(text, "${1}"+redacted)
	return urlPasswords.ReplaceAllString(text, "${1}"+redacted+"${3}")
}

func anyOf(alternatives ...string) string {
	out := ""
	for i, a := range alternatives {
		if i > 0 {
			out += "|"
		}
		out += "(?:" + a + ")"
	}
	return out
}
