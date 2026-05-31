package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSession_MissingFileReturnsZeroNoError(t *testing.T) {
	// First-run: no state.json yet. Loading must NOT error — there's
	// simply nothing to restore.
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := LoadSession(path)
	if err != nil {
		t.Fatalf("missing file should not error; got %v", err)
	}
	if st.Preset != "" || st.Sort != "" || st.CursorID != "" {
		t.Errorf("missing file should restore nothing; got %+v", st)
	}
	if st.Version != sessionVersion {
		t.Errorf("missing file should carry current version %d; got %d", sessionVersion, st.Version)
	}
}

func TestLoadSession_MalformedFileReturnsZeroWithError(t *testing.T) {
	// A corrupt state.json must degrade to "no restore" (zero value)
	// and surface an error for the caller to log — never fatal, never
	// a partial restore from garbage.
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := LoadSession(path)
	if err == nil {
		t.Error("malformed file should return an error for the caller to log")
	}
	if st.Preset != "" || st.Sort != "" || st.CursorID != "" {
		t.Errorf("malformed file should restore nothing; got %+v", st)
	}
}

func TestSaveLoadSession_Roundtrip(t *testing.T) {
	// Save then Load must reproduce every field. Also exercises the
	// first-time-save mkdir path (the parent dir doesn't exist yet).
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	want := SessionState{
		Version:  sessionVersion,
		Preset:   "human",
		Sort:     "deps",
		CursorID: "would-you-kindly-abc",
	}
	if err := SaveSession(path, want); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got != want {
		t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestSaveSession_DefaultsVersion(t *testing.T) {
	// A caller that forgets to stamp Version (zero value) must still
	// produce a file readable as the current version.
	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveSession(path, SessionState{Preset: "all"}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != sessionVersion || got.Preset != "all" {
		t.Errorf("got %+v, want version %d preset all", got, sessionVersion)
	}
}

func TestLoadSession_UnknownVersionIgnored(t *testing.T) {
	// A file written by a future wyk carries fields we can't trust;
	// the restore is ignored (zero value) but it's NOT an error —
	// session state is disposable and the next quit overwrites it.
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":999,"preset":"human"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := LoadSession(path)
	if err != nil {
		t.Errorf("unknown version should not error; got %v", err)
	}
	if st.Preset != "" {
		t.Errorf("unknown version should restore nothing; got %+v", st)
	}
}

func TestSessionDefaultPath_HonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	got, err := SessionDefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/tmp/xdg-test/wyk/state.json"; got != want {
		t.Errorf("SessionDefaultPath = %q, want %q", got, want)
	}
}
