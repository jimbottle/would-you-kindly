package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/updater"
)

// runUpdate handles `wyk update`. Checks GitHub for the latest
// release (cached, 24h TTL), reports the install command, and —
// unless -dry-run — runs `go install ...@<tag>` to pull it.
//
// Exit codes:
//
//	0  no-op (already current) or update succeeded
//	1  network / install / cache error
//	2  no PATH for `go` (can't install)
//	64 usage error
func runUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	yes := fs.Bool("y", false, "skip the [y/N] confirmation before running go install")
	dryRun := fs.Bool("dry-run", false, "print the install command without executing it")
	channel := fs.String("channel", "any", "release channel: `any` (include prereleases — default) or `stable` (skip prereleases)")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, _, err := updater.LatestCached(ctx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk update: cannot check for releases:", err)
		return 1
	}
	if rel.TagName == "" {
		fmt.Fprintln(os.Stderr, "wyk update: no releases known")
		return 1
	}
	if *channel == "stable" && rel.Prerelease {
		fmt.Fprintf(os.Stderr, "wyk update: latest release is a prerelease (%s); pass -channel any to install it anyway\n", rel.TagName)
		return 0
	}
	current := versionString()
	currentTag := extractCurrentTag(current)
	if !updater.IsNewer(currentTag, rel.TagName) {
		fmt.Printf("wyk update: already on %s (latest is %s)\n", currentTag, rel.TagName)
		return 0
	}
	cmd := updater.InstallCommand(rel.TagName)
	fmt.Printf("wyk update: %s → %s\n", currentTag, rel.TagName)
	fmt.Printf("            %s\n", cmd)
	if *dryRun {
		return 0
	}
	if !*yes {
		fmt.Print("            proceed? [y/N] ")
		ok, err := readYesNo(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk update:", err)
			return 1
		}
		if !ok {
			fmt.Println("            aborted")
			return 0
		}
	}
	if _, err := exec.LookPath("go"); err != nil {
		fmt.Fprintln(os.Stderr, "wyk update: `go` is not on PATH — install it from https://go.dev/dl, then re-run `wyk update -y`")
		return 2
	}
	// Run the install. Inherit stdout/stderr so the user sees Go's
	// progress and any compile errors. We pass through the literal
	// command from InstallCommand to keep one source of truth.
	parts := strings.Fields(cmd)
	c := exec.Command(parts[0], parts[1:]...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wyk update: install failed:", err)
		return 1
	}
	fmt.Printf("wyk update: installed %s — open a new shell or rehash $PATH if `wyk --version` still reports the old version\n", rel.TagName)
	return 0
}

// extractCurrentTag pulls the version token out of a versionString
// like "wyk v0.3.0-alpha (commit abcd123)". Returns "" if the
// string doesn't match what versionString() produces — caller
// falls through to the live-only path which treats empty as
// "older than anything tagged".
func extractCurrentTag(s string) string {
	s = strings.TrimPrefix(s, "wyk ")
	if i := strings.Index(s, " "); i > 0 {
		s = s[:i]
	}
	return s
}

// readUpdateNudge consults the cache (no live fetch) and returns a
// one-line banner suitable for the TUI or doctor when an update is
// available. Empty string when up-to-date or no cache exists.
// Designed to be cheap enough to call on every TUI paint without
// any I/O after the first cache read.
func readUpdateNudge(currentVer string) string {
	path, err := updater.CachePath()
	if err != nil {
		return ""
	}
	// Read directly to bypass the TTL — we want "what does the
	// most recent successful check say?" not "fetch live now".
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var entry struct {
		Latest updater.Release `json:"latest"`
	}
	if err := json.Unmarshal(b, &entry); err != nil {
		return ""
	}
	tag := entry.Latest.TagName
	if tag == "" {
		return ""
	}
	cur := extractCurrentTag(currentVer)
	if !updater.IsNewer(cur, tag) {
		return ""
	}
	return fmt.Sprintf("↑ wyk %s available — run `wyk update`", tag)
}

