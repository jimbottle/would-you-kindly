package main

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/jimbottle/would-you-kindly/internal/wykconfig"
)

// configKey describes one settable config.json key: how to read it
// from a loaded Config, how to validate+write a new value, and a
// one-line description for `wyk config list`. The table is the single
// source of truth so a future key slots in by adding one entry — the
// get/set/list dispatch iterate it rather than hard-coding `default_scope`.
type configKey struct {
	name string
	desc string
	// get returns the value stored in the file ("" when unset). The
	// effective default (when unset) is the caller's concern.
	get func(wykconfig.Config) string
	// set validates v and writes it into c. A validation failure is
	// returned verbatim so the CLI can print it and exit 64.
	set func(c *wykconfig.Config, v string) error
	// effectiveDefault is shown by `list`/`get` when the stored value
	// is empty, so a user sees what a command would actually use.
	effectiveDefault string
}

var configKeys = []configKey{
	{
		name: "default_scope",
		desc: "repos the multi-repo commands query by default (all | cwd)",
		get:  func(c wykconfig.Config) string { return c.DefaultScope },
		set: func(c *wykconfig.Config, v string) error {
			if err := wykconfig.ValidateScope(v); err != nil {
				return err
			}
			c.DefaultScope = v
			return nil
		},
		effectiveDefault: wykconfig.ScopeAll,
	},
}

// findConfigKey returns the configKey with the given name, or nil.
func findConfigKey(name string) *configKey {
	for i := range configKeys {
		if configKeys[i].name == name {
			return &configKeys[i]
		}
	}
	return nil
}

// runConfig dispatches `wyk config <sub>`. Mirrors `bd config get/set`
// so the machine-wide settings in ~/.config/wyk/config.json are
// editable without hand-editing JSON (hand-editing stays valid).
//
// Subcommands:
//
//	list                 print every key, its stored value, and the default
//	get <key>            print one key's value (the effective default when unset)
//	set <key> <value>    validate and persist a key
//
// Exit codes: 0 success, 1 load/save failure, 64 usage / bad value.
func runConfig(args []string) int {
	if len(args) == 0 {
		configUsage(os.Stderr)
		return 64
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runConfigList(rest)
	case "get":
		return runConfigGet(rest)
	case "set":
		return runConfigSet(rest)
	case "-h", "--help", "help":
		configUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "wyk config: unknown subcommand %q\n", sub)
		configUsage(os.Stderr)
		return 64
	}
}

func configUsage(w io.Writer) {
	fmt.Fprint(w, `usage: wyk config <subcommand>

Subcommands:
  list                 print every setting, its value, and the default
  get <key>            print one setting's value
  set <key> <value>    set a setting (validated, then persisted)

Keys:
`)
	for _, k := range sortedConfigKeys() {
		fmt.Fprintf(w, "  %-16s %s\n", k.name, k.desc)
	}
	fmt.Fprint(w, `
The config lives at ~/.config/wyk/config.json (XDG-aware). $WYK_DEFAULT_SCOPE
overrides default_scope for a single run.
`)
}

// sortedConfigKeys returns the key table in name order so list/usage
// output is deterministic regardless of declaration order.
func sortedConfigKeys() []configKey {
	out := make([]configKey, len(configKeys))
	copy(out, configKeys)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func runConfigList(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk config list")
		return 64
	}
	cfg, path, err := loadWykConfigForCmd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk config list:", err)
		return 1
	}
	fmt.Printf("# %s\n", path)
	for _, k := range sortedConfigKeys() {
		v := k.get(cfg)
		if v == "" {
			fmt.Printf("%-16s %s   (default; unset)\n", k.name, k.effectiveDefault)
		} else {
			fmt.Printf("%-16s %s\n", k.name, v)
		}
	}
	return 0
}

func runConfigGet(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: wyk config get <key>")
		return 64
	}
	key := findConfigKey(args[0])
	if key == nil {
		fmt.Fprintf(os.Stderr, "wyk config get: unknown key %q (run `wyk config list`)\n", args[0])
		return 64
	}
	cfg, _, err := loadWykConfigForCmd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk config get:", err)
		return 1
	}
	v := key.get(cfg)
	if v == "" {
		v = key.effectiveDefault
	}
	fmt.Println(v)
	return 0
}

func runConfigSet(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: wyk config set <key> <value>")
		return 64
	}
	key := findConfigKey(args[0])
	if key == nil {
		fmt.Fprintf(os.Stderr, "wyk config set: unknown key %q (run `wyk config list`)\n", args[0])
		return 64
	}
	cfg, path, err := loadWykConfigForCmd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk config set:", err)
		return 1
	}
	if err := key.set(&cfg, args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "wyk config set: %v\n", err)
		return 64
	}
	if err := wykconfig.Save(path, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "wyk config set:", err)
		return 1
	}
	fmt.Printf("set %s = %s in %s\n", key.name, args[1], path)
	return 0
}

// loadWykConfigForCmd resolves the config path and loads it, returning
// the path too so callers can name the file in errors and confirmations.
func loadWykConfigForCmd() (wykconfig.Config, string, error) {
	path, err := wykconfig.DefaultPath()
	if err != nil {
		return wykconfig.Config{}, "", err
	}
	cfg, err := wykconfig.Load(path)
	if err != nil {
		return wykconfig.Config{}, path, err
	}
	return cfg, path, nil
}
