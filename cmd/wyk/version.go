package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/updater"
)

// runVersion handles `wyk version` (and its `--version` / `-v`
// aliases). Without flags, prints the version line and exits 0
// — the historical behaviour. With `--check`, polls the release
// feed and reports up-to-date / newer-available / network-error,
// suitable for scripts and pre-commit hooks.
//
// Exit codes:
//
//	0  up-to-date (or version line printed)
//	1  newer release available (when --check set)
//	2  network / cache failure (when --check set)
//	64 usage error
func runVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	check := fs.Bool("check", false, "poll the release feed and exit 0 (current) / 1 (newer available) / 2 (network failure)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk version [--check]")
		return 64
	}
	if !*check {
		fmt.Println(versionString())
		return 0
	}
	return runVersionCheck()
}

// currentTagForCheck is the seam tests use to substitute a stable
// tag for the runtime's "(devel)" marker. Production reads
// versionString() through extractCurrentTag; tests override the
// var directly.
var currentTagForCheck = func() string { return extractCurrentTag(versionString()) }

// runVersionCheck does the live-fetch comparison. Honors a short
// timeout so a pre-commit hook doesn't hang on a flaky network.
// Honors the channel preference cached on disk via
// updater.CachedChannel so a stable-pinned user doesn't get a
// "newer available" exit from a prerelease.
func runVersionCheck() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rels, err := liveFetcher(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk version: cannot check for releases:", err)
		return 2
	}
	if len(rels) == 0 {
		fmt.Fprintln(os.Stderr, "wyk version: release feed empty")
		return 2
	}
	channel := updater.CachedChannel()
	current := currentTagForCheck()
	var rel updater.Release
	if channel == "stable" {
		rel = updater.PickStable(rels)
		// Stable-pinned + feed has no stable: report current and exit
		// 0. Falling back to rels[0] would nudge the user toward a
		// prerelease, defeating the exact guarantee the stable
		// channel promises.
		if rel.TagName == "" {
			fmt.Printf("wyk %s is current (no stable release in feed)\n", current)
			return 0
		}
	} else {
		rel = rels[0]
	}
	if updater.IsNewer(current, rel.TagName) {
		fmt.Printf("wyk %s → %s available — run `wyk update`\n", current, rel.TagName)
		return 1
	}
	fmt.Printf("wyk %s is current (latest %s is %s)\n", current, channel, rel.TagName)
	return 0
}
