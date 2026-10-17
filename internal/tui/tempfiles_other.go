//go:build !unix

package tui

// processAlive cannot tell on this platform; assume the process lives so
// nothing of another warte is removed.
func processAlive(int) bool { return true }
