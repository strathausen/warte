package tui

// Cheap argon2id parameters so lock tests finish well inside the harness's
// 150ms command budget. Production values live in lockfile.go.
func init() {
	lockArgon.Time = 1
	lockArgon.Memory = 64
	lockArgon.Threads = 1
}
