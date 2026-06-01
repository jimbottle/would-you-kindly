# wyk agent skills

`wyk` ships a small family of [Claude Code
skills](https://docs.claude.com/en/docs/claude-code/skills) — short,
on-demand instruction files that teach an agent *when* to reach for a
`wyk`/`bd` workflow and *which* command to run. They are embedded in the
`wyk` binary and installed with `wyk skills install`, so the version of a
skill always matches the version of the CLI it drives.

## The skills

| Skill                 | Trigger (what it's for)                                                                 |
| --------------------- | --------------------------------------------------------------------------------------- |
| `wyk`                 | General triage: check the agent inbox, find ready work, file/own/close bd issues the wyk way, and the owner + handoff conventions. |
| `wyk-handoff`         | Hand a task to a human — *when* handoff is the right call and *how* to write the runbook (the issue description) the human follows. |
| `wyk-project-review`  | Review the current beads against your actual context and flag where the plan has drifted (stale, mis-owned, wrongly-open, unowned). |

Each skill's body is **thin on purpose**: it calls the CLI (`wyk inbox`,
`wyk conventions`, `wyk handoff`, `wyk depgraph`, `bd …`) and treats the
CLI as the source of truth rather than restating conventions that would
rot. `wyk conventions` prints the canonical handoff contract; the skills
point at it instead of copying it.

## Installing

```bash
wyk skills install            # → ~/.claude/skills (all your Claude sessions)
wyk skills install -project   # → ./.claude/skills (this repo only)
wyk skills list               # show each skill + its install state
wyk skills print <name>       # dump one SKILL.md to stdout
wyk skills uninstall          # remove wyk's skills from the target
```

Install honours `$CLAUDE_CONFIG_DIR` (the same override Claude Code
respects) before falling back to `~/.claude`. Writes are atomic
(temp-file + rename) and idempotent; `-dry-run` prints the plan and `-y`
skips the confirmation.

You can also fold skill installation into the normal setup/maintenance
commands:

- `wyk init -skills` — install the skills while setting up a repo.
- `wyk doctor` — reports whether the skills are installed and current;
  `wyk doctor -fix` installs any that are missing.
- `wyk update` — reminds you to re-run `wyk skills install` with the new
  binary, since an upgrade may carry updated skills.

## Install state: current / out of date / modified

`wyk skills list` and `wyk doctor` classify each installed skill:

| State          | Meaning                                                                 | What install does           |
| -------------- | ----------------------------------------------------------------------- | --------------------------- |
| `not installed`| no copy at the target                                                   | writes it                   |
| `current`      | byte-identical to the embedded version                                  | leaves it                   |
| `out of date`  | an **unedited older** wyk version (a newer binary shipped a change)     | refreshes it (no `-force`)  |
| `modified`     | you hand-edited it                                                      | leaves it unless `-force`   |

The *out-of-date* vs *modified* distinction is what lets a post-`wyk
update` `wyk skills install` refresh the skills you haven't touched while
preserving the ones you have. It works via a hidden `.wyk-managed`
sidecar file written next to each `SKILL.md`: it records the sha256 of
exactly what wyk wrote. If the `SKILL.md` still hashes to that value,
nobody edited it, so any difference from the current embedded content is
purely a version bump (out of date). A hash mismatch — or no sidecar —
means it's been edited (modified), and wyk won't clobber it without
`-force`. The `SKILL.md` itself stays byte-for-byte the embedded content,
so the sidecar adds provenance without polluting what the agent reads.

## Keeping skills honest

Two tests guard the embedded skills against drift
(`cmd/wyk/skills_drift_test.go`):

- every `wyk <subcommand>` a skill names must be a real shipped
  subcommand — so a renamed or removed command can't keep being
  advertised to agents; and
- each skill's frontmatter `name:` must match its embed directory.

Combined with the install/idempotency/provenance tests in
`cmd/wyk/skills_cmd_test.go`, the family can't silently rot as the CLI
evolves.
