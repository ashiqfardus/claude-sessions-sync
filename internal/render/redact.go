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
	`ya29\.[0-9A-Za-z_\-]{20,}`,                                           // Google OAuth access token
	`(?:sk|rk)_live_[0-9A-Za-z]{20,}`,                                     // Stripe
	`hf_[A-Za-z0-9]{30,}`,                                                 // Hugging Face
	`npm_[A-Za-z0-9]{36}`,                                                 // npm
	`SG\.[A-Za-z0-9_\-]{16,}\.[A-Za-z0-9_\-]{16,}`,                        // SendGrid
	`eyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`, // JWT
	// PEM and PGP private keys, including one cut off before its END line - a
	// truncated tool result is exactly where that happens.
	`(?s)-----BEGIN [A-Z ]*PRIVATE KEY[A-Z ]*-----.*?(?:-----END [A-Z ]*PRIVATE KEY[A-Z ]*-----|\z)`,
))

// secretName is the name half of DB_PASSWORD=..., "apiKey": "...", OPENAI_KEY=...,
// Authorization: Bearer ..., Cookie: ..., and the same inside JSON-escaped tool input
// (\"password\":\"...\"), up to where the value starts.
const secretName = `(?i)([A-Za-z0-9_.\-]*` +
	`(?:secret|token|passw(?:or)?d|pwd|pass(?:phrase)?|api[_\-]?key|access[_\-]?key|account[_\-]?key|` +
	`private[_\-]?key|client[_\-]?secret|auth|credential|cookie|[_\-]key)` +
	`[A-Za-z0-9_.\-]*\\?["']?\s*[:=]\s*(?:bearer\s+|basic\s+)?`

// A quoted value is masked up to its closing quote, spaces included - an unquoted
// match stopped at the first space and left the rest of the password showing.
var (
	secretDoubleQuoted = regexp.MustCompile(secretName + `\\?")([^"\\\r\n]{4,})`)
	secretSingleQuoted = regexp.MustCompile(secretName + `')([^'\r\n]{4,})`)
	secretBare         = regexp.MustCompile(secretName + `)([^\s"'<>,;\\]{6,})`)
)

// urlPasswords masks the password in scheme://user:password@host, "/" included.
var urlPasswords = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://[^\s:/@]+:)([^\s@]+)(@)`)

// Redact masks credentials in text destined for a rendered page.
func Redact(text string) string {
	text = secretTokens.ReplaceAllString(text, redacted)
	text = secretDoubleQuoted.ReplaceAllString(text, "${1}"+redacted)
	text = secretSingleQuoted.ReplaceAllString(text, "${1}"+redacted)
	text = secretBare.ReplaceAllString(text, "${1}"+redacted)
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
