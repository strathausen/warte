//go:build unix

package media

import (
	"os"
	"syscall"
)

// pauseSupported reports whether external players can be paused (by
// stopping the process).
const pauseSupported = true

func signalPause(p *os.Process) error  { return p.Signal(syscall.SIGSTOP) }
func signalResume(p *os.Process) error { return p.Signal(syscall.SIGCONT) }
