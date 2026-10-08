//go:build unix

package hooks

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// Ctrl-C while a hook runs must kill the hook's whole process group and
// come back as ErrInterrupted — with Setpgid the hook is no longer in
// the terminal's foreground group, so the signal has to be forwarded by
// wyk itself. The test interrupts its own process; NotifyContext inside
// shellRunner catches it, so the test binary survives.
func TestShellRunner_InterruptKillsGroup(t *testing.T) {
	d := &Dispatcher{Config: Config{Command: `sleep 30 & wait`, Timeout: 30 * time.Second}}
	type res struct {
		fired bool
		err   error
	}
	done := make(chan res, 1)
	start := time.Now()
	go func() {
		_, fired, err := d.Dispatch(context.Background(), samplePayload())
		done <- res{fired, err}
	}()
	time.Sleep(300 * time.Millisecond) // let NotifyContext register and sh start
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("self-SIGINT: %v", err)
	}
	select {
	case r := <-done:
		if !r.fired || !errors.Is(r.err, ErrInterrupted) {
			t.Fatalf("fired=%v err=%v, want true, ErrInterrupted", r.fired, r.err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("took %s; the group was not killed on interrupt", time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Dispatch did not return after SIGINT")
	}
}
