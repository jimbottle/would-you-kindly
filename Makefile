# wyk Makefile. Intentionally narrow — `go build`, `go test`, and
# `golangci-lint run` cover the day-to-day. Targets here exist for
# tasks Go's default tooling doesn't: regenerating committed
# documentation snapshots the would-you-kindly.raylytics.io docs
# agent reads, and the matching drift check CI runs.

.PHONY: docs-snapshot docs-check help

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

help:
	@echo "Targets:"
	@echo "  docs-snapshot   regenerate docs/generated/{keymap.md,cli.md}"
	@echo "  docs-check      fail if docs/generated/ is stale (used by CI)"
