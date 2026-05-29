package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jimbottle/would-you-kindly/internal/tui"
)

// runHelp handles `wyk help`. The default behavior just routes
// the user to the in-TUI overlay (`?`), with one supported flag:
// `--markdown` emits the generated keymap reference for README /
// docs-site inclusion. Useful when the docs need to stay in sync
// with code without a manual copy step.
//
// Exit codes:
//
//	0  reference printed (or pointer-to-TUI message printed)
//	64 usage error
func runHelp(args []string) int {
	fs := flag.NewFlagSet("help", flag.ContinueOnError)
	asMarkdown := fs.Bool("markdown", false, "emit a markdown keymap reference (single source of truth: internal/tui.DocsKeymap)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk help [--markdown]")
		return 64
	}
	if *asMarkdown {
		fmt.Print(tui.KeymapMarkdown())
		return 0
	}
	// No flag: be useful but minimal. Point at the in-TUI overlay
	// for full context (binding chains, mouse hints, status legend).
	fmt.Println("wyk help: launch wyk and press `?` for the full key reference.")
	fmt.Println("  --markdown      emit the generated markdown reference")
	return 0
}
