# Agent Instructions

This project is **wyk** (`would-you-kindly`): a TUI over the `bd` (beads)
issue tracker, built around one convention — a task is a human's when it
carries the `human` label, and its description is the runbook the human
follows. wyk is developed with the same loop it implements, so everything
you do here is tracked in bd and handed off with `wyk handoff`.

Start here, in order:

1. **[`CLAUDE.md`](CLAUDE.md)** — the project's working conventions for
   agents: the Owner column contract, `make check` before pushing, the
   commit/`Closes:` trailer format, the status lifecycle, and the rule
   that friction you hit in wyk is wyk product feedback. It is written
   for Claude Code but every rule applies to any agent.
2. **`wyk conventions`** — the label contract, the agent-inbox query,
   and a filing example, straight from the binary (also
   [`docs/CONTRACT.md`](docs/CONTRACT.md)).
3. **`wyk inbox`** — tasks a human has bounced back to you. Work them.
4. **[`docs/WORKFLOW.md`](docs/WORKFLOW.md)** — the end-to-end
   agent ↔ human ↔ review loop this repo runs on, with real issue IDs.

Two rules that are easy to miss:

- File work with `wyk create …` (not bare `bd create`) so it carries
  `src:agent` and the session stamp; every issue needs an assignee.
- A task that needs a human **must** be handed off (`wyk handoff <id>`
  or `wyk handoff -create "…"`) — a task without the `human` label
  defaults to the agent and the human never sees it.

The block below is bd's standard integration boilerplate.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:7510c1e2 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
<!-- END BEADS INTEGRATION -->
