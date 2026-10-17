package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Plaintext temp files
//
// Two features write decrypted content to the system temp directory, both
// with mode 0600: $EDITOR drafts ("warte-edit-<pid>-*.md", removed as soon
// as the editor exits) and attachments opened in an external app
// ("warte-open-<pid>-*", removed after tempFileTTL or on quit). Each name
// carries the process id, so a later warte can remove files left behind by
// one that crashed or was killed (sweepStaleTemp) without touching those of
// another warte that is still running.

const (
	tempEditPrefix = "warte-edit-"
	tempOpenPrefix = "warte-open-"
)

// tempPattern is the os.CreateTemp pattern for this process.
func tempPattern(prefix, suffix string) string {
	return prefix + strconv.Itoa(os.Getpid()) + "-*" + suffix
}

// tempDir is where the plaintext temp files go.
func (a *App) tempDir() string {
	if a.opts.TempDir != "" {
		return a.opts.TempDir
	}
	return os.TempDir()
}

// sweepStaleTemp removes warte temp files in dir whose process is gone.
// It returns how many it removed.
func sweepStaleTemp(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	self := os.Getpid()
	n := 0
	for _, e := range entries {
		name := e.Name()
		var rest string
		switch {
		case strings.HasPrefix(name, tempEditPrefix):
			rest = name[len(tempEditPrefix):]
		case strings.HasPrefix(name, tempOpenPrefix):
			rest = name[len(tempOpenPrefix):]
		default:
			continue
		}
		pidStr, _, ok := strings.Cut(rest, "-")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 0 || pid == self || processAlive(pid) {
			continue
		}
		if info, err := e.Info(); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if os.Remove(filepath.Join(dir, name)) == nil {
			n++
		}
	}
	return n
}
