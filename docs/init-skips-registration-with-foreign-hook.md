# wyk bug: `init` aborts before registering when a foreign post-commit hook exists

> **Resolved.** `wyk init` now registers the repo *before* touching hooks
> and never gates registration on the hook outcome (see "Registration is
> never gated on the hook" in the README), and `wyk init
> -fix-foreign-hooks` chains wyk after every foreign hook across the
> registry. This file is kept as the write-up of how the bug presented
> and why the ordering is load-bearing.

**Severity:** high — the visible symptom is *"handoffs to the human never appear in the UI"*, not
*"a repo is missing from a list"*.

**Version**

```
wyk v0.7.1-0.20260701131153-d8cb4ca527e7 (commit d8cb4ca-dirty)
bd  1.0.4 (ce242a879)
git 2.53.0
macOS (darwin 25.5.0)
```

---

## Summary

`wyk init` does several mutations, then **aborts at the post-commit hook step** when it finds a
foreign hook — and never reaches repo registration. `wyk init -dry-run` on the same repo explicitly
says it **would** register. So the preview and the real run disagree about the one step that
determines whether the repo is visible to the UI at all.

Exit code is **64**, so a script can detect it; but a human running `wyk init` sees "refusing to
overwrite existing hook", reads it as a benign skip of an optional feature, and does not realise
the repo was never registered.

## Reproduction (clean repo, ~20s)

```sh
mkdir /tmp/wyktest && cd /tmp/wyktest && git init -q .
mkdir -p .beads/hooks
printf '#!/bin/sh\necho foreign\n' > .beads/hooks/post-commit
chmod +x .beads/hooks/post-commit
git config core.hooksPath .beads/hooks     # what `bd init` does
bd init

wyk init -dry-run     # says: "would register /tmp/wyktest in .../repos.json"
wyk init ; echo "exit=$?"
wyk registry list | grep wyktest || echo "NOT REGISTERED"
```

**Actual**

```
$ wyk init -dry-run
wyk init: would refuse to overwrite foreign hook at /tmp/wyktest/.beads/hooks/post-commit
  Re-run with -chain to keep both hooks, or -force to replace.
wyk init: would register /tmp/wyktest in /Users/…/.config/wyk/repos.json     <-- promises registration

$ wyk init
wyk init: appended wyk conventions to CLAUDE.md                              <-- mutation 1
wyk init: registered the bd-create-guard PreToolUse hook in .claude/settings.json  <-- mutation 2
wyk init: refusing to overwrite existing /tmp/wyktest/.beads/hooks/post-commit
  Use -chain to keep both hooks, or -force to replace.
exit=64
                                                                             <-- no registration line

$ wyk registry list | grep wyktest
NOT REGISTERED
```

**Expected:** registration happens (or, if aborting is intentional, `-dry-run` must not promise it).

## Why this is worse than it looks

Registration is what the **multi-repo** commands read — `inbox`, `dashboard`, `stats`, `activity`,
`export`, `depgraph`, and the TUI. But the **cwd-scoped** commands do *not* need it: `wyk handoff`,
`wyk create` and `bd` all work perfectly from inside an unregistered repo.

So the failure mode is silent and asymmetric:

- An agent runs `wyk handoff -create "…"` from the repo. It succeeds. It prints
  `handed <id> to human (2950-byte runbook)`.
- The issue really is created, really is labelled `human`, really is in that repo's bd.
- **The human never sees it**, because their UI only reads registered repos.

That is exactly what happened to us. Over several days an agent filed three handoffs — one of them
**P1, a live outage** — all reported as handed off, none ever visible in the UI. It surfaced only
because the human asked "is this project using wyk? I don't see any tasks for it".

The handoff contract is the product. A path where handoff reports success and the human is
structurally unable to receive it defeats it silently.

## Contributing factor: this is likely to hit *beads* repos specifically

`bd init` sets `core.hooksPath` to `.beads/hooks`. Anything else that installs a post-commit hook
there (in our case **roborev**, which auto-reviews every commit) is then "foreign" to wyk. So the
trigger isn't exotic — it's *"beads repo that also uses another commit-hook tool"*.

## Secondary bug: `wyk doctor` suggests a command that cannot work

`doctor` reports:

```
[WARN] repo home-assistant-pi: core.hooksPath redirect
       git's core.hooksPath redirects post-commit hooks to …/.beads/hooks, so wyk's hook in
       .git/hooks is bypassed … Re-run `(cd … && wyk init)` to install into the active hooks dir,
       or unset core.hooksPath.
```

But plain `wyk init` is precisely the command that refuses and does nothing here. Following
doctor's advice is a no-op that also silently fails to register. It should suggest `-chain` /
`-force`, or be resolved by fixing the primary bug.

Also worth reconsidering: doctor recommends `unset core.hooksPath` as a remedy. In a beads repo
that disables beads' own hooks (and, here, roborev's) — it is a fairly destructive suggestion to
offer without a warning.

## Suggested fix

Decouple registration from hook installation. Registration is cheap, idempotent, and has no
prerequisite relationship to hooks.

1. Register **first**, or at minimum continue to registration after declining the hook, and demote
   the hook refusal to a warning (exit 0 with a `[WARN]`, since nothing failed — wyk *chose* not to
   clobber).
2. If aborting really is intended, make `-dry-run` say so rather than promising registration, and
   say explicitly *"the repo will NOT be registered; it will be invisible to `wyk inbox`, the
   dashboard and the TUI"*.
3. Consider a `-skip-hook` flag. Today the only paths that reach registration are `-chain` and
   `-force`, both of which mutate hooks — there is **no** way to register a repo without touching
   them. (`wyk init -scan <root>` may be the exception; we did not need to test it once registered.)
4. Consider having `wyk handoff` warn when run in an unregistered repo — that is the moment the
   silent failure becomes consequential, and a one-line "note: this repo is not registered, the
   handoff will not appear in the UI — run `wyk init`" would have saved days here.

## Workaround, for anyone hitting this

Add the entry directly, which avoids `-chain`/`-force` touching hooks. **Append an
object to the existing `repos` array — do not replace the file.** `~/.config/wyk/repos.json`
is strict JSON (no comments) and has this shape, with every registered repo in one array:

```json
{
  "version": 1,
  "repos": [
    { "name": "some-existing-repo", "path": "/Users/you/Projects/some-existing-repo" },
    { "name": "my-repo", "path": "/abs/path/to/my-repo" }
  ]
}
```

Writing a bare `{ "name": ..., "path": ... }` as the whole file — which an earlier
draft of this section showed — drops **every** other registered repo (17 of them at
time of writing), reproducing the exact "repo is invisible to the UI" failure this
report is about, for all of them at once. A `//` comment will also fail the parse.

Safer than hand-editing, since it preserves the array and validates the JSON:

```sh
python3 - <<'EOF'
import json, pathlib
p = pathlib.Path.home() / ".config/wyk/repos.json"
d = json.loads(p.read_text())
entry = {"name": "my-repo", "path": "/abs/path/to/my-repo"}
if not any(r["path"] == entry["path"] for r in d["repos"]):
    d["repos"].append(entry)
    p.write_text(json.dumps(d, indent=2) + "\n")
EOF
```

Then `wyk registry list` and `wyk dashboard` pick it up immediately — no restart needed.
