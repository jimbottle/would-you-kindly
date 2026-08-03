package main

import (
	"flag"
	"fmt"
	"strings"
)

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
	Name    string // subcommand name, no "wyk " prefix
	Summary string // one-line description; sentence case, no trailing period
	Usage   string // canonical usage line, including "wyk <name>" prefix
	// Examples are common-case invocations ("command   # gloss"),
	// rendered in both `-h` (via subcommandUsage) and the generated
	// cli.md — the init -h treatment, for every subcommand
	// (would-you-kindly-rnjg).
	Examples []string
	Flags    []cliFlag // ordered as they appear in the runX
}

// cliFlag is one flag row in the per-subcommand table.
type cliFlag struct {
	Name        string // include the leading "-" (e.g. "-since")
	Default     string // empty string ⇒ rendered as "(empty)" in markdown
	Description string // same substance as the flag.String/Bool/Int call; see the parity note below
}

// cliSubcommandDocs is the canonical inventory. Order is the
// "Subcommands:" block in printTopLevelUsage so the generated
// page reads in the same order as the top-level help.
//
// Quoting convention: descriptions here render as MARKDOWN, so
// backticks around commands (`bd init`) are welcome and encouraged.
// The live FlagSet usage strings must NOT backtick multiword
// phrases — Go's flag package takes the first backquoted token as
// the flag's value placeholder, mangling -h output (enforced by
// TestSubcommandHelp_NoMultiwordFlagPlaceholders). So an entry here
// carries the same SUBSTANCE as its FlagSet twin, but quoting — and
// occasionally phrasing — may differ; only the FlagSet side is
// machine-checked, so verify against -h when syncing an entry.
var cliSubcommandDocs = []cliSubcommandDoc{
	{
		Name:    "handoff",
		Summary: "Hand a runbook to a human: tag the issue with `human`, set its description from stdin / -file.",
		Usage:   "wyk handoff [-C <dir>] [-file <path>] [-allow-empty] [-note <text>] [-identity name] [-dry-run] <issue-id>\n   or: wyk handoff -create \"<title>\" [-priority N] [-type task] [-identity name] [-file <path>] [-dry-run]\n   or: wyk handoff -template",
		Examples: []string{
			"cat runbook.md | wyk handoff -create \"Rotate the staging DB password\" -priority 1",
			"wyk handoff wyk-42 < runbook.md",
			"wyk handoff -template > runbook.md   # print the runbook skeleton to fill in",
		},
		Flags: []cliFlag{
			{Name: "-C", Default: "", Description: "run as if bd had been started in this directory"},
			{Name: "-file", Default: "", Description: "read the runbook from this file (default: stdin)"},
			{Name: "-allow-empty", Default: "false", Description: "permit an empty runbook (clears the issue's description). Required when stdin is a TTY."},
			{Name: "-create", Default: "", Description: "file a NEW bd issue with this title and hand it off; mutually exclusive with the <id> positional"},
			{Name: "-priority", Default: "1", Description: "priority for the newly-created issue (only used with -create; 0-4 or P0-P4)"},
			{Name: "-type", Default: "task", Description: "issue type for the newly-created issue (only used with -create)"},
			{Name: "-note", Default: "", Description: "after the handoff lands, append this one-line note to the issue (via bd note) — useful for 'back to you, see X' annotations without nuking the runbook"},
			{Name: "-identity", Default: "", Description: "route this handoff to the named agent identity (adds the src:agent:<name> label) so it lands in that identity's `wyk inbox` when bounced back; falls back to $WYK_AGENT_IDENTITY"},
			{Name: "-dry-run", Default: "false", Description: "print the runbook, labels, and destination ID that would be written without invoking bd; useful for verifying a runbook is well-formed before committing the human to it"},
			{Name: "-template", Default: "false", Description: "print the required 3-section runbook skeleton to stdout and exit (no bd writes); fill it in, then `wyk handoff <id> < filled.md`"},
		},
	},
	{
		Name:    "create",
		Summary: "File a bd issue (forwarding every flag to `bd create`) and stamp it with the Claude session that created it, so the TUI's Session column can trace work back to a conversation.",
		Usage:   "wyk create <bd create args...>",
		Examples: []string{
			"wyk create --title=\"Fix the flaky retry test\" --type=bug -p 2   # bd create + Claude session stamp",
		},
		Flags: nil, // every flag is forwarded verbatim to `bd create`; see `bd create --help`
	},
	{
		Name:    "init",
		Summary: "Install (or uninstall) the post-commit hook so commits with `Closes: <id>` trailers auto-close the referenced issue.",
		Usage:   "wyk init [-chain | -force] [-dry-run] [-skip-bd-init] [-skip-register] [-skip-claude-md] [-skills] [-scan <root>] [-uninstall] [-fix-foreign-hooks]",
		Examples: []string{
			"wyk init                  # bootstrap this repo (idempotent)",
			"wyk init -scan ~/Projects # register every bd workspace under a tree",
		},
		Flags: []cliFlag{
			{Name: "-force", Default: "false", Description: "overwrite an existing post-commit hook (destructive — drops the existing hook entirely)"},
			{Name: "-chain", Default: "false", Description: "preserve an existing post-commit hook and chain wyk's logic after it (preferred over -force when the existing hook is from another tool like roborev)"},
			{Name: "-dry-run", Default: "false", Description: "print what would happen without writing the hook"},
			{Name: "-skip-bd-init", Default: "false", Description: "do not run `bd init` even if .beads is missing"},
			{Name: "-skip-register", Default: "false", Description: "do not add this repo to ~/.config/wyk/repos.json"},
			{Name: "-skip-claude-md", Default: "false", Description: "do not seed the agent enrichment: wyk's conventions block in CLAUDE.md AND the bd-create-guard PreToolUse hook in .claude/settings.json (which redirects `bd create` to `wyk create`)"},
			{Name: "-scan", Default: "", Description: "scan this directory tree for existing bd workspaces and register every one found (skips repos already registered, hidden dirs, node_modules, vendor); mutually exclusive with the per-repo init path"},
			{Name: "-uninstall", Default: "false", Description: "remove wyk's post-commit hook (restoring post-commit.pre-wyk if present); refuses on foreign hooks"},
			{Name: "-fix-foreign-hooks", Default: "false", Description: "scan the registered repos for foreign post-commit hooks and chain wyk after each (idempotent; wyk-installed and missing hooks are left alone)"},
			{Name: "-skills", Default: "false", Description: "also install wyk's agent skills into ~/.claude/skills (idempotent; modified skills left alone)"},
		},
	},
	{
		Name:    "inbox",
		Summary: "Agent inbox: issues filed with `src:agent` that a human has bounced back.",
		Usage:   "wyk inbox [-C <dir>] [-all] [-json] [-compact] [-slim] [-priority N] [-repo name] [-limit N] [-identity name] [-strict]",
		Examples: []string{
			"wyk inbox          # what did the human bounce back to me?",
			"wyk inbox -json    # structured, for agent ingestion",
		},
		Flags: []cliFlag{
			{Name: "-C", Default: "", Description: "scope to a single workspace; default is the configured scope (every registered repo unless default_scope=cwd — see 'wyk config')"},
			{Name: "-all", Default: "false", Description: "query every registered repo, ignoring the configured default scope"},
			{Name: "-json", Default: "false", Description: "emit a JSON {issues, errors} envelope for LLM consumption (errors names any repos that failed, so a partial multi-repo result is labelled not silently truncated)"},
			{Name: "-compact", Default: "false", Description: "with -json, emit non-indented JSON (smaller; indentation is overhead for an LLM)"},
			{Name: "-slim", Default: "false", Description: "drop the heavy description/notes bodies from each issue (with -json; keeps the lightweight metadata)"},
			{Name: "-priority", Default: "-1", Description: "cap the inbox at priority N or higher (lower number = higher priority; -1 disables)"},
			{Name: "-repo", Default: "", Description: "restrict the inbox to the registered repo with this name (mutually exclusive with -C/-all)"},
			{Name: "-limit", Default: "-1", Description: "cap the inbox at N rows (after priority/repo filtering; -1 disables)"},
			{Name: "-identity", Default: "", Description: "scope the inbox to a single agent identity (src:agent:<name>); falls back to $WYK_AGENT_IDENTITY, then the collective inbox when unset"},
			{Name: "-strict", Default: "false", Description: "with -identity, show ONLY work routed to that identity; default also includes un-routed collective work so it isn't stranded"},
		},
	},
	{
		Name:    "stats",
		Summary: "Aggregate snapshot across registered repos: counts by status, human-flagged splits, time-to-close.",
		Usage:   "wyk stats [-C <dir>] [-all] [-json] [-compact] [-repo name]",
		Examples: []string{
			"wyk stats          # handoff-loop heartbeat across registered repos",
			"wyk stats -json",
		},
		Flags: []cliFlag{
			{Name: "-C", Default: "", Description: "scope to a single workspace; default is the configured scope (every registered repo unless default_scope=cwd — see 'wyk config')"},
			{Name: "-all", Default: "false", Description: "query every registered repo, ignoring the configured default scope"},
			{Name: "-json", Default: "false", Description: "emit a JSON object suitable for scripting"},
			{Name: "-compact", Default: "false", Description: "with -json, emit non-indented JSON (smaller; indentation is overhead for an LLM)"},
			{Name: "-repo", Default: "", Description: "restrict the rollup to the registered repo with this name (mutually exclusive with -C/-all)"},
		},
	},
	{
		Name:    "doctor",
		Summary: "Checks bd / wyk on PATH, $EDITOR, audit-trail actor, XDG paths, agent skills, and per-repo .git / .beads / hook state.",
		Usage:   "wyk doctor [-json] [-fix [-dry-run]]",
		Examples: []string{
			"wyk doctor         # diagnose bd/wyk/hook/registry wiring",
			"wyk doctor -fix    # install missing hooks + skills",
		},
		Flags: []cliFlag{
			{Name: "-json", Default: "false", Description: "emit checks as a structured JSON object for CI / dashboard consumption"},
			{Name: "-fix", Default: "false", Description: "install wyk's post-commit hook in every registered repo whose hook is missing (foreign / wyk / chained hooks are left alone), and install any missing wyk agent skills into ~/.claude/skills"},
			{Name: "-dry-run", Default: "false", Description: "with -fix, print the plan without installing"},
		},
	},
	{
		Name:    "registry",
		Summary: "Add, list, remove, or prune entries in the wyk repo registry (~/.config/wyk/repos.json). An unregistered workspace is invisible to every multi-repo view, so issues filed there reach nobody.",
		Usage:   "wyk registry <add [path] | list | remove <name> | prune> [-broken] [-y] [-json]",
		Examples: []string{
			"wyk registry add        # register the workspace you're standing in",
			"wyk registry list",
			"wyk registry prune -y   # drop entries whose repo is gone",
		},
		Flags: []cliFlag{
			{Name: "-y", Default: "false", Description: "skip the [y/N] confirmation prompt on prune (for scripts)"},
			{Name: "-broken", Default: "false", Description: "on prune, also drop entries whose path exists but holds no bd workspace (probes bd; only definitive 'no workspace' results qualify, not timeouts)"},
			{Name: "-json", Default: "false", Description: "emit structured JSON instead of the human-readable list"},
		},
	},
	{
		Name:    "bugreport",
		Summary: "One-shot pasteable capture for triaging a field bug: wyk version, allowlisted env, full doctor verdicts, config.json + repos.json, and the tail of the crash/debug logs.",
		Usage:   "wyk bugreport [-tail N] [-o file]",
		Examples: []string{
			"wyk bugreport                 # print the report to stdout",
			"wyk bugreport -o report.txt   # write it to a file to attach",
		},
		Flags: []cliFlag{
			{Name: "-tail", Default: "50", Description: "lines of the crash/debug logs to include (0 omits them)"},
			{Name: "-o", Default: "", Description: "write the report to this file instead of stdout"},
		},
	},
	{
		Name:    "config",
		Summary: "Get/set machine-wide wyk settings in ~/.config/wyk/config.json (e.g. default_scope, which repos the multi-repo commands query by default).",
		Usage:   "wyk config <list | get <key> | set <key> <value>>",
		Examples: []string{
			"wyk config set default_scope cwd   # scope inbox/stats/… to the cwd's repo",
			"wyk config list",
		},
		Flags: nil,
	},
	{
		Name:    "conventions",
		Summary: "Print the agent-facing label convention (human, src:agent, inbox query).",
		Usage:   "wyk conventions [-json]",
		Examples: []string{
			"wyk conventions    # agents: run this before writing to bd here",
		},
		Flags: []cliFlag{
			{Name: "-json", Default: "false", Description: "emit a structured JSON object instead of the prose tip"},
		},
	},
	{
		Name:    "update",
		Summary: "Check for and install a newer wyk release. Live-fetches every invocation (no cache).",
		Usage:   "wyk update [-y] [-dry-run] [-channel any|stable]",
		Examples: []string{
			"wyk update                  # check + install the latest release",
			"wyk update -channel stable  # skip prereleases",
		},
		Flags: []cliFlag{
			{Name: "-y", Default: "false", Description: "skip the [y/N] confirmation before running go install"},
			{Name: "-dry-run", Default: "false", Description: "print the install command without executing it"},
			{Name: "-channel", Default: "any", Description: "release channel: `any` (include prereleases — default) or `stable` (skip prereleases). When omitted, the most recently used channel is reused so a stable-pinned user clicking the TUI's nudge doesn't silently jump back to prereleases."},
		},
	},
	{
		Name:    "dashboard",
		Summary: "Per-repo rollup of open / human-flagged / recently-closed counts.",
		Usage:   "wyk dashboard [-all] [-json] [-compact] [-days N] [-repo name] [-priority N]",
		Examples: []string{
			"wyk dashboard      # per-repo open/human/closed-this-week table",
		},
		Flags: []cliFlag{
			{Name: "-all", Default: "false", Description: "query every registered repo, ignoring the configured default scope"},
			{Name: "-json", Default: "false", Description: "emit a structured JSON object instead of the table"},
			{Name: "-compact", Default: "false", Description: "with -json, emit non-indented JSON (smaller; indentation is overhead for an LLM)"},
			{Name: "-days", Default: "7", Description: "window for the closed-recently column (default 7)"},
			{Name: "-repo", Default: "", Description: "restrict the rollup to the registered repo with this name (mutually exclusive with -all)"},
			{Name: "-priority", Default: "-1", Description: "drop issues below priority N before tallying counts (lower number = higher priority; -1 disables — does NOT hide empty repo rows)"},
		},
	},
	{
		Name:    "export",
		Summary: "JSON dump of every registered repo's open issue list + ready IDs (-closed for full history).",
		Usage:   "wyk export [-all] [-since 24h] [-compact] [-slim] [-closed] [-repo name]",
		Examples: []string{
			"wyk export > wyk-backup.json",
		},
		Flags: []cliFlag{
			{Name: "-all", Default: "false", Description: "query every registered repo, ignoring the configured default scope"},
			{Name: "-since", Default: "", Description: "filter issues to those updated within this duration (e.g. 24h, 168h)"},
			{Name: "-compact", Default: "false", Description: "emit non-indented JSON (smaller; better for piping into jq / streaming consumers)"},
			{Name: "-slim", Default: "false", Description: "drop the heavy description/notes bodies from each issue (keeps id/title/status/priority/labels); ~75%+ smaller for an LLM scanning the backlog"},
			{Name: "-closed", Default: "false", Description: "include closed issues (default: open issues only — the actionable set)"},
			{Name: "-repo", Default: "", Description: "restrict the dump to the registered repo with this name (mutually exclusive with -all)"},
		},
	},
	{
		Name:    "import",
		Summary: "Restore from a `wyk export` dump: closed-in-dump skipped; open issues create-if-missing or diff-apply-if-existing.",
		Usage:   "wyk import [-file path] [-dry-run] [-repo name]",
		Examples: []string{
			"wyk import -file wyk-backup.json -dry-run   # preview the reconcile",
		},
		Flags: []cliFlag{
			{Name: "-file", Default: "", Description: "path to JSON dump (default: read from stdin)"},
			{Name: "-dry-run", Default: "false", Description: "print the plan without touching bd"},
			{Name: "-repo", Default: "", Description: "restrict the reconcile to the dump entry with this name (empty = every entry)"},
		},
	},
	{
		Name:    "activity",
		Summary: "Recently-touched issues across registered repos (chronological merged stream).",
		Usage:   "wyk activity [-all] [-since 24h] [-json] [-compact] [-priority N] [-repo name] [-status open|closed|all] [-limit N]",
		Examples: []string{
			"wyk activity -since 24h   # what changed across repos today?",
		},
		Flags: []cliFlag{
			{Name: "-all", Default: "false", Description: "query every registered repo, ignoring the configured default scope"},
			{Name: "-since", Default: "24h", Description: "show issues updated within this duration (e.g. 1h, 24h, 168h)"},
			{Name: "-json", Default: "false", Description: "emit a structured JSON array instead of the table"},
			{Name: "-compact", Default: "false", Description: "with -json, emit non-indented JSON (smaller; indentation is overhead for an LLM)"},
			{Name: "-priority", Default: "-1", Description: "cap rows at priority N or higher (lower number = higher priority; -1 disables)"},
			{Name: "-repo", Default: "", Description: "restrict the stream to the registered repo with this name (mutually exclusive with -all)"},
			{Name: "-status", Default: "all", Description: "filter rows by status: open / closed / all"},
			{Name: "-limit", Default: "-1", Description: "cap the stream at N rows (after every other filter; -1 disables)"},
		},
	},
	{
		Name:    "depgraph",
		Summary: "Cross-repo dependency graph between bd issues: text tree (default), Graphviz DOT, or {nodes,edges} JSON.",
		Usage:   "wyk depgraph [-all] [-dot | -json] [-compact] [-repo name] [-priority N] [-closed]",
		Examples: []string{
			"wyk depgraph                          # cross-repo dependency tree",
			"wyk depgraph -dot | dot -Tsvg > deps.svg",
		},
		Flags: []cliFlag{
			{Name: "-all", Default: "false", Description: "query every registered repo, ignoring the configured default scope"},
			{Name: "-dot", Default: "false", Description: "emit Graphviz DOT (pipe into `dot -Tsvg`)"},
			{Name: "-json", Default: "false", Description: "emit {nodes, edges} JSON for tooling consumers"},
			{Name: "-compact", Default: "false", Description: "with -json, emit non-indented JSON (smaller; indentation is overhead for an LLM)"},
			{Name: "-repo", Default: "", Description: "restrict to the registered repo with this name (mutually exclusive with -all)"},
			{Name: "-priority", Default: "-1", Description: "only include issues at this priority or higher (0=critical; -1=all); the cap is per-node, so an edge to a lower-priority neighbor is pruned and a high-priority issue with only lower-priority links can drop out"},
			{Name: "-closed", Default: "false", Description: "include closed issues (default omits them)"},
		},
	},
	{
		Name:    "skills",
		Summary: "Install the wyk agent skills (wyk / wyk-handoff / wyk-project-review) into ~/.claude/skills so a harness loads them on demand.",
		Usage:   "wyk skills <list | install | uninstall | print <name>> [-user | -project] [-force] [-dry-run] [-y]",
		Examples: []string{
			"wyk skills install   # put the wyk agent skills in ~/.claude/skills",
		},
		Flags: []cliFlag{
			{Name: "-user", Default: "false", Description: "target the user skills dir ~/.claude/skills (the default; honors $CLAUDE_CONFIG_DIR)"},
			{Name: "-project", Default: "false", Description: "target the project skills dir ./.claude/skills instead of the user dir"},
			{Name: "-force", Default: "false", Description: "on install, overwrite a locally-modified skill (default leaves modified copies untouched)"},
			{Name: "-dry-run", Default: "false", Description: "print the install/uninstall plan without touching disk"},
			{Name: "-y", Default: "false", Description: "skip the confirmation prompt on install/uninstall"},
		},
	},
	{
		Name:    "help",
		Summary: "Pointer at the in-TUI `?` overlay; opt-in flags emit markdown references for the docs site.",
		Usage:   "wyk help [--markdown] [--cli-markdown]",
		Examples: []string{
			"wyk help --markdown > keymap.md   # the TUI keymap as markdown",
		},
		Flags: []cliFlag{
			{Name: "--markdown", Default: "false", Description: "emit a markdown keymap reference (single source of truth: internal/tui.DocsKeymap)"},
			{Name: "--cli-markdown", Default: "false", Description: "emit a markdown CLI-flag reference for every subcommand (single source of truth: cliSubcommandDocs)"},
		},
	},
	{
		Name:    "completion",
		Summary: "Emit a shell completion script for bash, zsh, or fish.",
		Usage:   "wyk completion <bash|zsh|fish>",
		Examples: []string{
			"eval \"$(wyk completion bash)\"",
		},
		Flags: nil,
	},
	{
		Name:    "version",
		Summary: "Print the version line. With --check, polls the release feed and exits 0/1/2/64.",
		Usage:   "wyk version [--check]",
		Examples: []string{
			"wyk version --check   # exit 1 when a newer release exists",
		},
		Flags: []cliFlag{
			{Name: "--check", Default: "false", Description: "poll the release feed and exit 0 (current) / 1 (newer available) / 2 (network failure)"},
		},
	},
}

// findCLIDoc returns the cliSubcommandDocs entry for name, or nil.
// Nested dispatchers (registry list, skills install, ...) look up
// their parent's entry — the doc covers every subform.
func findCLIDoc(name string) *cliSubcommandDoc {
	if base, _, ok := strings.Cut(name, " "); ok {
		name = base
	}
	for i := range cliSubcommandDocs {
		if cliSubcommandDocs[i].Name == name {
			return &cliSubcommandDocs[i]
		}
	}
	return nil
}

// subcommandUsage returns the fs.Usage hook that gives a subcommand's
// -h the init -h treatment (would-you-kindly-rnjg): a one-line
// summary, the canonical usage, common-case examples, then the flag
// table — instead of Go's bare "Usage of <name>:" flag dump. Content
// comes from cliSubcommandDocs, the same source the generated cli.md
// uses, so -h and the published reference cannot drift on the
// synopsis. A subcommand with no doc entry (the internal `hook`)
// falls back to a plain usage line.
func subcommandUsage(fs *flag.FlagSet, name string) func() {
	return func() {
		// The FlagSet's configured output, NOT a hardcoded stderr —
		// PrintDefaults below writes there, and splitting the help
		// block across two streams would be silent breakage for any
		// future SetOutput (roborev #2060).
		w := fs.Output()
		doc := findCLIDoc(name)
		if doc == nil {
			fmt.Fprintf(w, "usage: wyk %s [flags]\n\nFlags:\n", name)
			fs.PrintDefaults()
			return
		}
		fmt.Fprintf(w, "wyk %s — %s\n\nUsage:\n", doc.Name, doc.Summary)
		for _, line := range strings.Split(doc.Usage, "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
		if len(doc.Examples) > 0 {
			fmt.Fprint(w, "\nCommon case:\n")
			for _, e := range doc.Examples {
				fmt.Fprintf(w, "  %s\n", e)
			}
		}
		fmt.Fprint(w, "\nFlags:\n")
		fs.PrintDefaults()
	}
}
