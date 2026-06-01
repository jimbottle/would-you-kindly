# wyk Makefile. `make check` is the one local gate that mirrors the
# `test` CI workflow — run it before pushing. The rest are the pieces
# it composes plus tasks Go's default tooling doesn't cover: the
# committed doc snapshots the would-you-kindly.raylytics.io docs agent
# reads, and the plugin-skills sync.

.PHONY: docs-snapshot docs-check plugin-skills plugin-skills-check lint check help

# Regenerate the markdown snapshots under docs/generated/. Build a
# fresh binary into /tmp so this target works from a dirty tree
# without polluting ./bin or relying on `go run` per call (which
# re-compiles every invocation).
docs-snapshot:
	@mkdir -p docs/generated
	@go build -o /tmp/wyk-docgen ./cmd/wyk
	@/tmp/wyk-docgen help --markdown > docs/generated/keymap.md
	@/tmp/wyk-docgen help --cli-markdown > docs/generated/cli.md
	@rm -f /tmp/wyk-docgen
	@echo "docs-snapshot: docs/generated/{keymap.md,cli.md} regenerated"

# Drift check: regenerate the snapshots and fail if any committed
# file changed OR if a snapshot file is untracked. Use
# `git status --porcelain` rather than `git diff --quiet` because
# the latter only surfaces modifications to tracked files — a
# future docs-snapshot that emits a new file would silently pass
# the diff check while the file sits uncommitted.
docs-check: docs-snapshot
	@status=$$(git status --porcelain -- docs/generated/); \
	if [ -n "$$status" ]; then \
		echo "docs-check: docs/generated/ is stale — run 'make docs-snapshot' and commit the result"; \
		echo "$$status"; \
		git diff -- docs/generated/; \
		exit 1; \
	fi
	@echo "docs-check: docs/generated/ is up to date"

# Sync the Claude Code plugin's bundled skills from the embedded
# source of truth (internal/skills/data). The plugin under plugin/ ships
# real SKILL.md files because a marketplace install can't reach into the
# wyk binary — so they're committed copies. This target regenerates them;
# TestPluginSkillsMatchEmbedded fails if a copy drifts from the embedded
# content.
plugin-skills:
	@rm -rf plugin/skills
	@for d in internal/skills/data/*/; do \
		name=$$(basename $$d); \
		mkdir -p plugin/skills/$$name; \
		cp $$d/SKILL.md plugin/skills/$$name/SKILL.md; \
	done
	@echo "plugin-skills: plugin/skills/ synced from internal/skills/data/"

# Drift check mirroring docs-check: regenerate the bundled skills and
# fail if anything changed or is untracked.
plugin-skills-check: plugin-skills
	@status=$$(git status --porcelain -- plugin/skills/); \
	if [ -n "$$status" ]; then \
		echo "plugin-skills-check: plugin/skills/ is stale — run 'make plugin-skills' and commit"; \
		echo "$$status"; \
		exit 1; \
	fi
	@echo "plugin-skills-check: plugin/skills/ is up to date"

# golangci-lint pinned to the version CI runs (.github/workflows/test.yml,
# .golangci.yml: govet, staticcheck, errcheck, ineffassign, unused).
# Catches what `go vet` alone misses — the gate that sat red-but-unwatched
# in CI because the local gates didn't run it.
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "lint: golangci-lint not found — install the CI-pinned version:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2"; \
		exit 1; \
	}
	@golangci-lint run

# The full local gate. Runs the same checks as the `test` CI workflow,
# in the same order, so a green `make check` means a green push — the
# missing link that let a golangci-lint failure ship unnoticed. Also runs
# the plugin-skills drift guard (a local check CI doesn't yet run).
check:
	@echo "==> gofmt"; out=$$(gofmt -l .); if [ -n "$$out" ]; then \
		echo "gofmt: non-conforming files (run \`gofmt -w .\`):"; echo "$$out"; exit 1; fi
	@$(MAKE) --no-print-directory docs-check
	@echo "==> go vet"; go vet ./...
	@$(MAKE) --no-print-directory lint
	@echo "==> go build"; go build ./...
	@$(MAKE) --no-print-directory plugin-skills-check
	@echo "==> go test -race"; go test -race -timeout 5m ./...
	@echo "check: all local gates passed (mirrors the test CI workflow)"

help:
	@echo "Targets:"
	@echo "  check                run the full local gate (mirrors the test CI workflow) — use before pushing"
	@echo "  lint                 golangci-lint run (CI-pinned version)"
	@echo "  docs-snapshot        regenerate docs/generated/{keymap.md,cli.md}"
	@echo "  docs-check           fail if docs/generated/ is stale (run by CI + make check)"
	@echo "  plugin-skills        sync plugin/skills/ from internal/skills/data/"
	@echo "  plugin-skills-check  fail if plugin/skills/ is stale (run by make check)"
