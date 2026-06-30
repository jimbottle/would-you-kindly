package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/wykconfig"
)

// bugreportEnvNames are the non-prefixed environment variables worth
// including in a report. Combined with the WYK_ / XDG_ prefix sweep, this
// is a strict allowlist — we never dump the full environment, so secrets
// (tokens, keys) in unrelated vars can't leak into a pasted report.
var bugreportEnvNames = []string{"NO_COLOR", "BEADS_ACTOR", "EDITOR", "TERM", "COLORTERM"}

// runBugreport implements `wyk bugreport`: a one-shot, pasteable capture
// of everything useful for triaging a field bug — wyk version, the
// allowlisted environment, the full `wyk doctor` verdicts (which runs the
// live per-repo bd probes), the config + registry files, and the tail of
// the crash/debug logs. Prints to stdout by default; -o writes a file.
//
// Exit codes: 0 success, 1 write failure, 64 usage error.
func runBugreport(args []string) int {
	fs := flag.NewFlagSet("bugreport", flag.ContinueOnError)
	fs.Usage = subcommandUsage(fs, "bugreport")
	tail := fs.Int("tail", 50, "lines of the crash/debug logs to include (0 omits them)")
	outPath := fs.String("o", "", "write the report to this file instead of stdout")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk bugreport [-tail N] [-o file]")
		return 64
	}

	var b strings.Builder
	writeBugreport(&b, *tail)

	if *outPath != "" {
		if err := os.WriteFile(*outPath, []byte(b.String()), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "wyk bugreport:", err)
			return 1
		}
		fmt.Printf("wrote bug report to %s\n", *outPath)
		return 0
	}
	fmt.Print(b.String())
	return 0
}

// writeBugreport renders the full report into w. Pure-ish (reads files +
// env + runs doctor) so it can be unit-tested by inspecting the string.
func writeBugreport(w io.Writer, tail int) {
	fmt.Fprintln(w, "# wyk bug report")
	fmt.Fprintf(w, "version: %s\n", versionString())
	fmt.Fprintln(w, "(environment is an allowlist — WYK_*/XDG_* + a few named vars — so no unrelated secrets are included)")

	fmt.Fprintln(w, "\n## environment")
	for _, kv := range bugreportEnv() {
		fmt.Fprintf(w, "  %s\n", kv)
	}

	fmt.Fprintln(w, "\n## doctor")
	for _, c := range collectDoctorChecks() {
		// Mirrors the doctor text row, minus color, so the verdicts read
		// the same in a pasted report as on a terminal.
		if c.detail != "" {
			fmt.Fprintf(w, "  [%s] %s — %s\n", c.status, c.name, c.detail)
		} else {
			fmt.Fprintf(w, "  [%s] %s\n", c.status, c.name)
		}
	}

	fmt.Fprintln(w, "\n## config.json")
	writeFileSection(w, configPathForReport())
	fmt.Fprintln(w, "\n## repos.json")
	writeFileSection(w, registryPathForReport())

	if tail > 0 {
		fmt.Fprintf(w, "\n## bd error log (last %d lines)\n", tail)
		writeFileTail(w, errorLogPath(), tail)
		fmt.Fprintf(w, "\n## crash log (last %d lines)\n", tail)
		writeFileTail(w, crashLogPath(), tail)
		if dbg := debugLogPath(); dbg != "" {
			fmt.Fprintf(w, "\n## debug log (last %d lines)\n", tail)
			writeFileTail(w, dbg, tail)
		}
	}
}

// bugreportEnv returns the allowlisted environment as "KEY=value" lines,
// sorted for deterministic output.
func bugreportEnv() []string {
	named := make(map[string]bool, len(bugreportEnvNames))
	for _, n := range bugreportEnvNames {
		named[n] = true
	}
	var out []string
	for _, e := range os.Environ() {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "WYK_") || strings.HasPrefix(key, "XDG_") || named[key] {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		out = []string{"(none set)"}
	}
	return out
}

// configPathForReport / registryPathForReport resolve the two state-file
// paths, swallowing the (rare) path-resolution error to a sentinel string
// so the report still renders rather than aborting.
func configPathForReport() string {
	p, err := wykconfig.DefaultPath()
	if err != nil {
		return ""
	}
	return p
}

func registryPathForReport() string {
	p, err := registry.DefaultPath()
	if err != nil {
		return ""
	}
	return p
}

// writeFileSection dumps a small state file verbatim, or a clear marker
// when it's absent/unreadable. Used for config.json and repos.json, which
// are small and carry no secrets.
func writeFileSection(w io.Writer, path string) {
	if path == "" {
		fmt.Fprintln(w, "  (path could not be resolved)")
		return
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(w, "  (not present: %s)\n", path)
		return
	}
	if err != nil {
		fmt.Fprintf(w, "  (unreadable: %v)\n", err)
		return
	}
	fmt.Fprintf(w, "  # %s\n", path)
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		fmt.Fprintf(w, "  %s\n", line)
	}
}

// writeFileTail writes the last n lines of path, or a marker when it's
// absent. Reads the whole file (logs are bounded once rotation lands and
// are small in practice) then slices the tail.
func writeFileTail(w io.Writer, path string, n int) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(w, "  (none: %s)\n", path)
		return
	}
	if err != nil {
		fmt.Fprintf(w, "  (unreadable: %v)\n", err)
		return
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, line := range lines {
		fmt.Fprintf(w, "  %s\n", line)
	}
}
