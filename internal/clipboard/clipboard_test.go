package clipboard

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestWriteOSC52_EmitsExpectedWireFormat(t *testing.T) {
	// Pin the literal bytes Copy would emit so a regression in
	// the escape sequence (BEL→ST, dropping the introducer,
	// changing the clipboard target from `c`) breaks this test.
	// Without this, the prior version of the file tested only
	// base64.StdEncoding + the test's own concatenation — a
	// tautology that left the production format unchecked.
	var buf bytes.Buffer
	if err := writeOSC52(&buf, "wyk-3sz"); err != nil {
		t.Fatalf("writeOSC52: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("wyk-3sz"))
	want := "\x1b]52;c;" + encoded + "\x07"
	if got := buf.String(); got != want {
		t.Errorf("wire format = %q, want %q", got, want)
	}
}

func TestWriteOSC52_EmptyStringClearsClipboard(t *testing.T) {
	// Empty text is still a well-formed OSC 52 — some terminals
	// interpret an empty payload as "clear the clipboard". The
	// wire format should be `\x1b]52;c;\x07` (no base64 body).
	var buf bytes.Buffer
	if err := writeOSC52(&buf, ""); err != nil {
		t.Fatalf("writeOSC52(\"\"): %v", err)
	}
	if got, want := buf.String(), "\x1b]52;c;\x07"; got != want {
		t.Errorf("empty-payload wire format = %q, want %q", got, want)
	}
}

// failingWriter always returns the supplied error on Write so the
// error-propagation contract can be tested without mocking
// /dev/tty itself.
type failingWriter struct{ err error }

func (f failingWriter) Write(_ []byte) (int, error) { return 0, f.err }

func TestWriteOSC52_PropagatesWriteError(t *testing.T) {
	sentinel := errors.New("disk full")
	err := writeOSC52(failingWriter{err: sentinel}, "anything")
	if err == nil {
		t.Fatalf("expected an error when the writer fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("returned error should wrap the writer's; got %v", err)
	}
	if !strings.Contains(err.Error(), "write OSC 52") {
		t.Errorf("error should name the OSC 52 context; got %q", err.Error())
	}
}
