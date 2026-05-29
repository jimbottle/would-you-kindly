package main

import (
	"fmt"
	"io"
	"os"
)

// wykSubcommands is the user-facing subcommand list that ships in
// the generated shell completion scripts. Kept in sync with
// main.go's dispatch switch by convention.
//
// Ordering mirrors the switch in main.go (not the prose order in
// printTopLevelUsage). The two USED to claim to match — they
// don't, and the dispatch order is the source of truth.
//
// Intentionally excluded: `hook`. It's invoked by the installed
// git post-commit hook, not by humans on the command line, and
// completing to it would be misleading. Other top-level switch
// cases (`version`, `--version`, `-v`) are aliases and only the
// canonical `version` is listed.
var wykSubcommands = []string{
	"handoff",
	"init",
	"inbox",
	"stats",
	"doctor",
	"registry",
	"conventions",
	"update",
	"dashboard",
	"export",
	"import",
	"activity",
	"help",
	"completion",
	"version",
}

// runCompletion handles `wyk completion <shell>` — emits a
// completion script for the named shell to stdout. The script
// surfaces only the top-level subcommands; per-subcommand flag
// completion is out of scope (each subcommand has flag.PrintDefaults
// for the actual reference, and most users only need help finding
// the verbs).
//
// Exit codes:
//
//	0  script emitted
//	64 usage error (missing or unknown shell)
func runCompletion(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk completion <bash|zsh|fish>")
		return 64
	}
	switch args[0] {
	case "bash":
		emitBashCompletion(os.Stdout)
	case "zsh":
		emitZshCompletion(os.Stdout)
	case "fish":
		emitFishCompletion(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "wyk completion: unknown shell %q (try bash, zsh, or fish)\n", args[0])
		return 64
	}
	return 0
}

// emitBashCompletion writes a bash completion script. Users source
// it into their shell:
//
//	eval "$(wyk completion bash)"
//
// or pipe it into a file under /etc/bash_completion.d.
func emitBashCompletion(w io.Writer) {
	fmt.Fprint(w, `# wyk bash completion. Source via:
#   eval "$(wyk completion bash)"
# Or write to /etc/bash_completion.d/wyk.
_wyk() {
    local cur
    cur="${COMP_WORDS[COMP_CWORD]}"
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=( $(compgen -W "`)
	for i, c := range wykSubcommands {
		if i > 0 {
			fmt.Fprint(w, " ")
		}
		fmt.Fprint(w, c)
	}
	fmt.Fprint(w, `" -- "$cur") )
    fi
    return 0
}
complete -F _wyk wyk
`)
}

// emitZshCompletion writes a zsh completion script. Drop it into
// a directory on $fpath as `_wyk` (no extension) and zsh picks
// it up on the next shell start; or `eval "$(wyk completion zsh)"`
// for an ephemeral session.
func emitZshCompletion(w io.Writer) {
	fmt.Fprint(w, `#compdef wyk
# wyk zsh completion. Install via:
#   wyk completion zsh > "${fpath[1]}/_wyk"
# Or eval for the current session:
#   eval "$(wyk completion zsh)"
_wyk() {
    local -a cmds
    cmds=(`)
	for _, c := range wykSubcommands {
		fmt.Fprintf(w, "\n        '%s'", c)
	}
	fmt.Fprint(w, `
    )
    _arguments \
        '1: :->cmd' \
        '*::arg:->args'
    case $state in
        cmd) _describe -t commands 'wyk subcommand' cmds ;;
    esac
}
_wyk "$@"
`)
}

// emitFishCompletion writes a fish completion script. fish picks
// up completions under ~/.config/fish/completions/wyk.fish without
// any sourcing step:
//
//	wyk completion fish > ~/.config/fish/completions/wyk.fish
func emitFishCompletion(w io.Writer) {
	fmt.Fprint(w, `# wyk fish completion. Install via:
#   wyk completion fish > ~/.config/fish/completions/wyk.fish
function __fish_wyk_no_subcommand
    set -l cmd (commandline -opc)
    test (count $cmd) -eq 1
end

`)
	for _, c := range wykSubcommands {
		fmt.Fprintf(w, "complete -c wyk -n __fish_wyk_no_subcommand -f -a %s\n", c)
	}
}
