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

const bashCompletion = `# bash completion for claude-sessions
_claude_sessions() {
  local cur prev commands
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"
  commands="ls search stats doctor config completion version help"

  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
    return
  fi

  case "${COMP_WORDS[1]}" in
    ls)     COMPREPLY=( $(compgen -W "--project --since --until --limit --json --claude-dir" -- "$cur") ) ;;
    search) COMPREPLY=( $(compgen -W "--project --role --regexp --limit --json --claude-dir" -- "$cur") ) ;;
    stats)  COMPREPLY=( $(compgen -W "--json --claude-dir" -- "$cur") ) ;;
    doctor) COMPREPLY=( $(compgen -W "--archive --json --claude-dir" -- "$cur") ) ;;
    config)
      if [ "$COMP_CWORD" -eq 2 ]; then
        COMPREPLY=( $(compgen -W "show set-destination" -- "$cur") )
      else
        COMPREPLY=( $(compgen -d -- "$cur") )
      fi
      ;;
    completion) COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") ) ;;
  esac
  [ -n "$prev" ] || true
}
complete -F _claude_sessions claude-sessions
`

const zshCompletion = `#compdef claude-sessions
_claude-sessions() {
  local -a commands
  commands=(
    'ls:list sessions on this machine'
    'search:search the text of every session'
    'stats:session counts and sizes per project'
    'doctor:check archive, hook, retention and manifest drift'
    'config:show or set the archive destination'
    'completion:print a shell completion script'
    'version:print the version'
  )
  if (( CURRENT == 2 )); then
    _describe 'command' commands
    return
  fi
  case "$words[2]" in
    ls)     _arguments '--project=[filter by path]' '--since=[YYYY-MM-DD]' '--until=[YYYY-MM-DD]' '--limit=[max rows]' '--json' '--claude-dir=[profile]:directory:_files -/' ;;
    search) _arguments '--project=[filter by path]' '--role=[user or assistant]' '--regexp' '--limit=[max hits]' '--json' ;;
    doctor) _arguments '--archive=[folder]:directory:_files -/' '--json' ;;
    config) _values 'action' 'show' 'set-destination' ;;
    completion) _values 'shell' 'bash' 'zsh' 'fish' ;;
  esac
}
_claude-sessions "$@"
`

const fishCompletion = `# fish completion for claude-sessions
complete -c claude-sessions -f
complete -c claude-sessions -n __fish_use_subcommand -a ls         -d 'list sessions on this machine'
complete -c claude-sessions -n __fish_use_subcommand -a search     -d 'search the text of every session'
complete -c claude-sessions -n __fish_use_subcommand -a stats      -d 'session counts and sizes per project'
complete -c claude-sessions -n __fish_use_subcommand -a doctor     -d 'check archive, hook, retention, manifest'
complete -c claude-sessions -n __fish_use_subcommand -a config     -d 'show or set the archive destination'
complete -c claude-sessions -n __fish_use_subcommand -a completion -d 'print a shell completion script'
complete -c claude-sessions -n __fish_use_subcommand -a version    -d 'print the version'

complete -c claude-sessions -n '__fish_seen_subcommand_from ls'     -l project -l since -l until -l limit -l json -l claude-dir
complete -c claude-sessions -n '__fish_seen_subcommand_from search' -l project -l role -l regexp -l limit -l json
complete -c claude-sessions -n '__fish_seen_subcommand_from doctor' -l archive -l json -l claude-dir
complete -c claude-sessions -n '__fish_seen_subcommand_from config' -a 'show set-destination'
complete -c claude-sessions -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
`
