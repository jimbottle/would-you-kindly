package filters

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPath_XDGOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/xdg", "wyk", "filters.json")
	if p != want {
		t.Errorf("DefaultPath = %q, want %q", p, want)
	}
}

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	a, err := Load(filepath.Join(dir, "nope.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", a.Version, CurrentVersion)
	}
	if len(a.Aliases) != 0 {
		t.Errorf("Aliases should be empty; got %v", a.Aliases)
	}
}

func TestLoad_ParsesAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filters.json")
	body := `{"version":1,"aliases":{"blocked":"status=blocked","p0":"p0"}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Aliases["blocked"] != "status=blocked" || a.Aliases["p0"] != "p0" {
		t.Errorf("Aliases = %v, want blocked/p0 entries", a.Aliases)
	}
}

func TestLoad_VersionMismatchSentinel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filters.json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected version-mismatch error")
	}
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("expected ErrUnsupportedVersion sentinel; got %v", err)
	}
}

func TestLookup_HitAndMiss(t *testing.T) {
	a := Aliases{Aliases: map[string]string{"blocked": "status=blocked"}}
	if q, ok := a.Lookup("@blocked"); !ok || q != "status=blocked" {
		t.Errorf("Lookup(@blocked) = (%q, %v), want (status=blocked, true)", q, ok)
	}
	if _, ok := a.Lookup("@missing"); ok {
		t.Errorf("Lookup(@missing) should miss")
	}
	if _, ok := a.Lookup("noprefix"); ok {
		t.Errorf("Lookup(noprefix) should miss (no @ prefix)")
	}
	if _, ok := a.Lookup("@"); ok {
		t.Errorf("Lookup(@) should miss (empty name)")
	}
}
