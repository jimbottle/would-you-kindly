//go:build unix

package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Ctrl-C while a hook runs must kill the hook's whole process group and
// come back as ErrInterrupted — with Setpgid the hook is no longer in
// the terminal's foreground group, so the signal has to be forwarded by
// wyk itself. The test interrupts its own process; NotifyContext inside
// shellRunner catches it, so the test binary survives.
//
// Two things the shell script makes checkable: it writes its child's
// pid, which doubles as the "handler is registered" marker (NotifyContext
// runs before cmd.Start, so once the file exists the SIGINT is safe to
// send), and after Dispatch returns that pid must be gone — WaitDelay
// alone would also bring Run back in ~2s, so timing cannot tell a killed
// group from an orphaned `sleep`.
func TestShellRunner_InterruptKillsGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	d := &Dispatcher{Config: Config{
		Command: `sleep 30 & echo $! > "$PIDFILE"; wait`,
		Timeout: 30 * time.Second,
	}}
	t.Setenv("PIDFILE", pidFile)

	type res struct {
		fired bool
		err   error
	}
	done := make(chan res, 1)
	go func() {
		_, fired, err := d.Dispatch(context.Background(), samplePayload())
		done <- res{fired, err}
	}()

	childPid := waitForPidFile(t, pidFile)
	start := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("self-SIGINT: %v", err)
	}
	select {
	case r := <-done:
		if !r.fired || !errors.Is(r.err, ErrInterrupted) {
			t.Fatalf("fired=%v err=%v, want true, ErrInterrupted", r.fired, r.err)
		}
		if elapsed := time.Since(start); elapsed > waitDelayAfterCancel {
			t.Fatalf("took %s after SIGINT; a killed group returns well inside WaitDelay (%s)", elapsed, waitDelayAfterCancel)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Dispatch did not return after SIGINT")
	}
	assertProcessGone(t, childPid)
}

// The timeout path must kill the forked child too, not just sh.
func TestShellRunner_TimeoutKillsChildProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	d := &Dispatcher{Config: Config{
		Command: `sleep 30 & echo $! > "$PIDFILE"; wait`,
		Timeout: 300 * time.Millisecond,
	}}
	t.Setenv("PIDFILE", pidFile)
	start := time.Now()
	_, fired, err := d.Dispatch(context.Background(), samplePayload())
	if !fired || !errors.Is(err, ErrTimedOut) {
		t.Fatalf("fired=%v err=%v, want true, ErrTimedOut", fired, err)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond+waitDelayAfterCancel {
		t.Fatalf("took %s; a killed group returns well inside the timeout plus WaitDelay", elapsed)
	}
	assertProcessGone(t, waitForPidFile(t, pidFile))
}

func waitForPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hook never wrote its child pid to %s", path)
	return 0
}

// assertProcessGone polls briefly (the group was SIGKILLed; init reaps
// the orphan a moment later) and fails if the pid still answers signal 0.
func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL) // do not leave it behind on failure
	t.Fatalf("child %d survived: the process group was not killed", pid)
}
