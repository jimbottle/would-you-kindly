//go:build !unix

package hooks

import "os/exec"

// isolateProcessGroup is a no-op off Unix: there is no Setpgid, and the
// default CommandContext cancel (kill the process) plus WaitDelay is the
// best available. Windows is not a supported wyk target; this exists so
// `GOOS=windows go build ./...` keeps compiling, as it did before the
// hook package arrived.
func isolateProcessGroup(*exec.Cmd) {}
