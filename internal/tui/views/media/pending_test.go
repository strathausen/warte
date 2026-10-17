package media

import (
	"os"
	"testing"
	"time"
)

// TestStopAllRemovesPendingTemp: decrypted copies handed to an external
// opener are removed on exit even before their timer fires.
func TestStopAllRemovesPendingTemp(t *testing.T) {
	path, err := writeTemp([]byte("secret"), ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", st.Mode().Perm())
	}
	removeAfter(path, time.Hour)
	StopAll()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp file still exists after StopAll: %v", err)
	}
}
