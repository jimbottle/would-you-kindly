package main

// cliSubcommandDoc captures one row of the user-facing CLI
// reference emitted by `wyk help --cli-markdown`. Hand-maintained
// because Go's flag package doesn't carry the "intended usage
// line" or a human summary at the FlagSet level — extracting both
// requires either reflection over every runX or per-subcommand
// helpers. Drift is caught by the docs-snapshot drift check in
// CI: a flag added to a runX without a matching update here
// produces a stale committed snapshot, which the check rejects.
//
// When adding a new subcommand or flag:
//  1. Add the flag to the runX in the usual place.
//  2. Add or update the matching entry below.
//  3. Run `make docs-snapshot` and commit docs/generated/cli.md.
type cliSubcommandDoc struct {
	Name    string     // subcommand name, no "wyk " prefix
	Summary string     // one-line description; sentence case, no trailing period
	Usage   string     // canonical usage line, including "wyk <name>" prefix
	Flags   []cliFlag  // ordered as they appear in the runX
}

// cliFlag is one flag row in the per-subcommand table.
type cliFlag struct {
	Name        string // include the leading "-" (e.g. "-since")
	Default     string // empty string ⇒ rendered as "(empty)" in markdown
	Description string // verbatim from the flag.String/Bool/Int call
}

// cliSubcommandDocs is the canonical inventory. Order is the
// "Subcommands:" block in printTopLevelUsage so the generated
// page reads in the same order as the top-level help.
var cliSubcommandDocs = []cliSubcommandDoc{
	{
		Name:    "handoff",
		Summary: "Hand a runbook to a human: tag the issue with `human`, set its description from stdin / -file.",
		Usage:   "wyk handoff [-C <dir>] [-file <path>] [-allow-empty] [-note <text>] <issue-id>\n   or: wyk handoff -create \"<title>\" [-priority N] [-type task] [-file <path>]",
		Flags: []cliFlag{
			{Name: "-C", Default: "", Description: "run as if bd had been started in this directory"},
			{Name: "-file", Default: "", Description: "read the runbook from this file (default: stdin)"},
			{Name: "-allow-empty", Default: "false", Description: "permit an empty runbook (clears the issue's description). Required when stdin is a TTY."},
			{Name: "-create", Default: "", Description: "file a NEW bd issue with this title and hand it off; mutually exclusive with the <id> positional"},
			{Name: "-priority", Default: "1", Description: "priority for the newly-created issue (only used with -create; 0-4 or P0-P4)"},
			{Name: "-type", Default: "task", Description: "issue type for the newly-created issue (only used with -create)"},
			{Name: "-note", Default: "", Description: "after the handoff lands, append this one-line note to the issue (via bd note) — useful for 'back to you, see X' annotations without nuking the runbook"},
		},
	},
	{
		Name:    "init",
		Summary: "Install (or uninstall) the post-commit hook so commits with `Closes: <id>` trailers auto-close the referenced issue.",
		Usage:   "wyk init [-chain | -force] [-dry-run] [-skip-bd-init] [-skip-register] [-scan <root>] [-uninstall]",
		Flags: []cliFlag{
			{Name: "-force", Default: "false", Description: "overwrite an existing post-commit hook (destructive — drops the existing hook entirely)"},
			{Name: "-chain", Default: "false", Description: "preserve an existing post-commit hook and chain wyk's logic after it (preferred over -force when the existing hook is from another tool like roborev)"},
			{Name: "-dry-run", Default: "false", Description: "print what would happen without writing the hook"},
			{Name: "-skip-bd-init", Default: "false", Description: "do not run `bd init` even if .beads is missing"},
			{Name: "-skip-register", Default: "false", Description: "do not add this repo to ~/.config/wyk/repos.json"},
			{Name: "-scan", Default: "", Description: "scan this directory tree for existing bd workspaces and register every one found (skips repos already registered, hidden dirs, node_modules, vendor); mutually exclusive with the per-repo init path"},
			{Name: "-uninstall", Default: "false", Description: "remove wyk's post-commit hook (restoring post-commit.pre-wyk if present); refuses on foreign hooks"},
		},
	},
	{
		Name:    "inbox",
		Summary: "Agent inbox: issues filed with `src:agent` that a human has bounced back.",
		Usage:   "wyk inbox [-C <dir>] [-json] [-priority N] [-repo name]",
		Flags: []cliFlag{
			{Name: "-C", Default: "", Description: "scope to a single workspace; default is every registered repo"},
			{Name: "-json", Default: "false", Description: "emit a JSON array of issues for LLM consumption"},
			{Name: "-priority", Default: "-1", Description: "cap the inbox at priority N or higher (lower number = higher priority; -1 disables)"},
			{Name: "-repo", Default: "", Description: "restrict the inbox to the registered repo with this name (mutually exclusive with -C)"},
		},
	},
	{
		Name:    "stats",
		Summary: "Aggregate snapshot across registered repos: counts by status, human-flagged splits, time-to-close.",
		Usage:   "wyk stats [-C <dir>] [-json] [-repo name]",
		Flags: []cliFlag{
			{Name: "-C", Default: "", Description: "scope to a single workspace; default is every registered repo"},
			{Name: "-json", Default: "false", Description: "emit a JSON object suitable for scripting"},
			{Name: "-repo", Default: "", Description: "restrict the rollup to the registered repo with this name (mutually exclusive with -C)"},
		},
	},
	{
		Name:    "doctor",
		Summary: "Checks bd / wyk on PATH, $EDITOR, audit-trail actor, XDG paths, and per-repo .git / .beads / hook state.",
		Usage:   "wyk doctor [-json]",
		Flags: []cliFlag{
			{Name: "-json", Default: "false", Description: "emit checks as a structured JSON object for CI / dashboard consumption"},
		},
	},
	{
		Name:    "registry",
		Summary: "List, remove, or prune entries in the wyk repo registry (~/.config/wyk/repos.json).",
		Usage:   "wyk registry <list | remove <name> | prune> [-y] [-json]",
		Flags: []cliFlag{
			{Name: "-y", Default: "false", Description: "skip the [y/N] confirmation prompt on prune (for scripts)"},
			{Name: "-json", Default: "false", Description: "emit structured JSON instead of the human-readable list"},
		},
	},
	{
		Name:    "conventions",
		Summary: "Print the agent-facing label convention (human, src:agent, inbox query).",
		Usage:   "wyk conventions [-json]",
		Flags: []cliFlag{
			{Name: "-json", Default: "false", Description: "emit a structured JSON object instead of the prose tip"},
		},
	},
	{
		Name:    "update",
		Summary: "Check for and install a newer wyk release. Live-fetches every invocation (no cache).",
		Usage:   "wyk update [-y] [-dry-run] [-channel any|stable]",
		Flags: []cliFlag{
			{Name: "-y", Default: "false", Description: "skip the [y/N] confirmation before running go install"},
			{Name: "-dry-run", Default: "false", Description: "print the install command without executing it"},
			{Name: "-channel", Default: "any", Description: "release channel: `any` (include prereleases — default) or `stable` (skip prereleases). When omitted, the most recently used channel is reused so a stable-pinned user clicking the TUI's nudge doesn't silently jump back to prereleases."},
		},
	},
	{
		Name:    "dashboard",
		Summary: "Per-repo rollup of open / human-flagged / recently-closed counts.",
		Usage:   "wyk dashboard [-json] [-days N] [-repo name]",
		Flags: []cliFlag{
			{Name: "-json", Default: "false", Description: "emit a structured JSON object instead of the table"},
			{Name: "-days", Default: "7", Description: "window for the closed-recently column (default 7)"},
			{Name: "-repo", Default: "", Description: "restrict the rollup to the registered repo with this name (empty = every registered repo)"},
		},
	},
	{
		Name:    "export",
		Summary: "JSON dump of every registered repo's full issue list + ready IDs.",
		Usage:   "wyk export [-since 24h] [-compact] [-repo name]",
		Flags: []cliFlag{
			{Name: "-since", Default: "", Description: "filter issues to those updated within this duration (e.g. 24h, 168h)"},
			{Name: "-compact", Default: "false", Description: "emit non-indented JSON (smaller; better for piping into jq / streaming consumers)"},
			{Name: "-repo", Default: "", Description: "restrict the dump to the registered repo with this name (empty = full registry)"},
		},
	},
	{
		Name:    "import",
		Summary: "Restore from a `wyk export` dump: closed-in-dump skipped; open issues create-if-missing or diff-apply-if-existing.",
		Usage:   "wyk import [-file path] [-dry-run] [-repo name]",
		Flags: []cliFlag{
			{Name: "-file", Default: "", Description: "path to JSON dump (default: read from stdin)"},
			{Name: "-dry-run", Default: "false", Description: "print the plan without touching bd"},
			{Name: "-repo", Default: "", Description: "restrict the reconcile to the dump entry with this name (empty = every entry)"},
		},
	},
	{
		Name:    "activity",
		Summary: "Recently-touched issues across registered repos (chronological merged stream).",
		Usage:   "wyk activity [-since 24h] [-json] [-priority N] [-repo name] [-status open|closed|all]",
		Flags: []cliFlag{
			{Name: "-since", Default: "24h", Description: "show issues updated within this duration (e.g. 1h, 24h, 168h)"},
			{Name: "-json", Default: "false", Description: "emit a structured JSON array instead of the table"},
			{Name: "-priority", Default: "-1", Description: "cap rows at priority N or higher (lower number = higher priority; -1 disables)"},
			{Name: "-repo", Default: "", Description: "restrict the stream to the registered repo with this name (empty = every registered repo)"},
			{Name: "-status", Default: "all", Description: "filter rows by status: open / closed / all"},
		},
	},
	{
		Name:    "help",
		Summary: "Pointer at the in-TUI `?` overlay; opt-in flags emit markdown references for the docs site.",
		Usage:   "wyk help [--markdown] [--cli-markdown]",
		Flags: []cliFlag{
			{Name: "--markdown", Default: "false", Description: "emit a markdown keymap reference (single source of truth: internal/tui.DocsKeymap)"},
			{Name: "--cli-markdown", Default: "false", Description: "emit a markdown CLI-flag reference for every subcommand (single source of truth: cliSubcommandDocs)"},
		},
	},
	{
		Name:    "completion",
		Summary: "Emit a shell completion script for bash, zsh, or fish.",
		Usage:   "wyk completion <bash|zsh|fish>",
		Flags:   nil,
	},
	{
		Name:    "version",
		Summary: "Print the version line. With --check, polls the release feed and exits 0/1/2/64.",
		Usage:   "wyk version [--check]",
		Flags: []cliFlag{
			{Name: "--check", Default: "false", Description: "poll the release feed and exit 0 (current) / 1 (newer available) / 2 (network failure)"},
		},
	},
}
