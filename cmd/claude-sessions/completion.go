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
	default:
		fmt.Fprint(os.Stderr, `usage: claude-sessions completion <bash|zsh|fish>

  bash:  claude-sessions completion bash > /etc/bash_completion.d/claude-sessions
  zsh:   claude-sessions completion zsh  > "${fpath[1]}/_claude-sessions"
  fish:  claude-sessions completion fish > ~/.config/fish/completions/claude-sessions.fish
`)
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
        COMPREPLY=( $(compgen -W "show set-destination" -- "$cur") ); return
      fi ;;
    completion) COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") ); return ;;
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
    config) if (( CURRENT == 3 )); then compadd show set-destination; return; fi ;;
    completion) compadd bash zsh fish; return ;;
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
complete -c claude-sessions -n '__fish_seen_subcommand_from config' -a 'show set-destination'
complete -c claude-sessions -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
complete -c claude-sessions -n 'not __fish_use_subcommand' -a "(claude-sessions (commandline -opc)[2] -h 2>&1 | ` + listFlags + `)"
`
