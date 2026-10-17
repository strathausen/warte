//go:build unix

package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartAudioExec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.ogg")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// sh -c 'sleep 0.3' x <path>: the file becomes $1.
	p, err := StartAudio([]string{"sh", "-c", "sleep 0.3", "x"}, path)
	if err != nil {
		t.Skip("no sh:", err)
	}
	if err := p.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := p.Resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
	}
	p.Kill() // after exit: harmless

	q, err := StartAudio([]string{"sleep", "10"}, path)
	if err != nil {
		t.Skip(err)
	}
	_ = q.Pause()
	q.Kill()
	select {
	case <-q.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("paused process survived Kill")
	}
}
