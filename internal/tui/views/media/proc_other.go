//go:build !unix

package media

import (
	"errors"
	"os"
)

// pauseSupported reports whether external players can be paused; on this
// platform pausing stops playback instead.
const pauseSupported = false

func signalPause(*os.Process) error  { return errors.New("pause is not supported") }
func signalResume(*os.Process) error { return errors.New("resume is not supported") }
