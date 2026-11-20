package main

import (
	"fmt"
	"os"
)

func cmdCompletion(args []string) error {
	shell := ""
	if len(args) > 0 {
		shell = args[0]
	}

	switch shell {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	case "powershell", "pwsh":
		fmt.Print(powershellCompletion)
	default:
		fmt.Fprint(os.Stderr, `usage: claude-sessions completion <bash|zsh|fish|powershell>

  bash:        claude-sessions completion bash > ~/.local/share/bash-completion/completions/claude-sessions
  zsh:         claude-sessions completion zsh  > "${fpath[1]}/_claude-sessions"
  fish:        claude-sessions completion fish > ~/.config/fish/completions/claude-sessions.fish
  powershell:  claude-sessions completion powershell | Out-String | Add-Content $PROFILE
`)
		if shell == "-h" || shell == "--help" || shell == "help" {
			return nil // asking for help is not a failure
		}
		return fmt.Errorf("specify a shell")
	}
	return nil
}

// The scripts ask the binary for its commands (`help`) and each command's flags
// (`<command> -h`) at completion time. The hand-written lists they replaced covered
// six of fifteen commands - push, install, render, import and the rest were missing -
// because nothing kept them in step with the code.

const listCommands = `awk '/^commands:/{f=1;next} /^$/{f=0} f{print $1}'`
const listFlags = `awk '$1 ~ /^-/ {print "-" $1}'`

const bashCompletion = `# bash completion for claude-sessions
_claude_sessions() {
  local cur="${COMP_WORDS[COMP_CWORD]}" bin="${COMP_WORDS[0]}"
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W "$("$bin" help 2>&1 | ` + listCommands + `)" -- "$cur") )
    return
  fi
  case "${COMP_WORDS[1]}" in
    config)
      if [ "$COMP_CWORD" -eq 2 ]; then
        COMPREPLY=( $(compgen -W "show set-destination map unmap set-machine" -- "$cur") ); return
      fi ;;
    completion) COMPREPLY=( $(compgen -W "bash zsh fish powershell" -- "$cur") ); return ;;
  esac
  if [[ "$cur" == -* ]]; then
    COMPREPLY=( $(compgen -W "$("$bin" "${COMP_WORDS[1]}" -h 2>&1 | ` + listFlags + `)" -- "$cur") )
  else
    COMPREPLY=( $(compgen -f -- "$cur") )
  fi
}
complete -F _claude_sessions claude-sessions
`

const zshCompletion = `#compdef claude-sessions
_claude-sessions() {
  local -a items
  if (( CURRENT == 2 )); then
    items=(${(f)"$($words[1] help 2>&1 | ` + listCommands + `)"})
    compadd -a items
    return
  fi
  case "$words[2]" in
    config) if (( CURRENT == 3 )); then compadd show set-destination map unmap set-machine; return; fi ;;
    completion) compadd bash zsh fish powershell; return ;;
  esac
  if [[ $PREFIX == -* ]]; then
    items=(${(f)"$($words[1] $words[2] -h 2>&1 | ` + listFlags + `)"})
    compadd -a items
  else
    _files
  fi
}
_claude-sessions "$@"
`

const fishCompletion = `# fish completion for claude-sessions
complete -c claude-sessions -f
complete -c claude-sessions -n __fish_use_subcommand -a "(claude-sessions help 2>&1 | ` + listCommands + `)"
complete -c claude-sessions -n '__fish_seen_subcommand_from config' -a 'show set-destination map unmap set-machine'
complete -c claude-sessions -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish powershell'
complete -c claude-sessions -n 'not __fish_use_subcommand' -a "(claude-sessions (commandline -opc)[2] -h 2>&1 | ` + listFlags + `)"
`

// powershellCompletion works in Windows PowerShell 5.1 as well as pwsh: Windows is
// where most installs are. Returning nothing lets PowerShell complete file names.
const powershellCompletion = `# PowerShell completion for claude-sessions
Register-ArgumentCompleter -Native -CommandName claude-sessions, claude-sessions.exe -ScriptBlock {
    param($word, $ast, $cursor)
    # The typed text up to the cursor, not the AST: the command's extent drops a
    # trailing space, and PowerShell parses a bare -- specially.
    $full = $ast.Extent.StartScriptPosition.GetFullScript()
    $start = $ast.Extent.StartOffset
    $upTo = $full.Substring($start, [Math]::Max(0, [Math]::Min($full.Length, $cursor) - $start))
    $words = @(-split $upTo)
    $word = if ($upTo -match '\s$') { '' } else { $words[-1] }
    $pos = if ($word) { $words.Count - 1 } else { $words.Count }
    $bin = $words[0]
    $items = @()
    if ($pos -le 1) {
        # Windows PowerShell turns blank stderr lines into error records, so the command
        # block ends at the first line that is not an indented command.
        $in = $false
        foreach ($line in (& $bin help 2>&1 | ForEach-Object { "$_" })) {
            if ($line -match '^commands:') { $in = $true; continue }
            if (-not $in) { continue }
            if ($line -match '^  (\S+)') { $items += $Matches[1] } else { $in = $false }
        }
    } elseif ($words[1] -eq 'config' -and $pos -eq 2) {
        $items = 'show', 'set-destination', 'map', 'unmap', 'set-machine'
    } elseif ($words[1] -eq 'completion') {
        $items = 'bash', 'zsh', 'fish', 'powershell'
    } elseif ($word -like '-*') {
        foreach ($line in (& $bin $words[1] -h 2>&1 | ForEach-Object { "$_" })) {
            if ($line -match '^\s*-(\S+)') { $items += '--' + $Matches[1] }
        }
    }
    $items | Where-Object { $_ -like "$word*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`
