package config

import (
	"os"
	"path/filepath"
	"testing"
)

// setOS overrides goos for the duration of the test.
func setOS(t *testing.T, os string) {
	t.Helper()
	old := goos
	goos = os
	t.Cleanup(func() { goos = old })
}

func clearXDG(t *testing.T) {
	t.Helper()
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", EnvConfig} {
		t.Setenv(k, "")
	}
}

func TestPathsLinux(t *testing.T) {
	setOS(t, "linux")
	clearXDG(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	p, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	want := Paths{
		ConfigDir:  filepath.Join(home, ".config", "warte"),
		ConfigFile: filepath.Join(home, ".config", "warte", "config.toml"),
		DataDir:    filepath.Join(home, ".local", "share", "warte"),
		CacheDir:   filepath.Join(home, ".cache", "warte"),
		StateDir:   filepath.Join(home, ".local", "state", "warte"),
		LogFile:    filepath.Join(home, ".local", "state", "warte", "warte.log"),
	}
	if p != want {
		t.Errorf("paths\n got %+v\nwant %+v", p, want)
	}
	if lp, _ := LogPath(); lp != want.LogFile {
		t.Errorf("LogPath = %q", lp)
	}
}

func TestPathsXDG(t *testing.T) {
	for _, goosName := range []string{"linux", "darwin"} {
		t.Run(goosName, func(t *testing.T) {
			setOS(t, goosName)
			clearXDG(t)
			t.Setenv("HOME", t.TempDir())
			base := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "cfg"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
			p, err := ResolvePaths()
			if err != nil {
				t.Fatal(err)
			}
			tests := []struct{ got, want string }{
				{p.ConfigDir, filepath.Join(base, "cfg", "warte")},
				{p.DataDir, filepath.Join(base, "data", "warte")},
				{p.CacheDir, filepath.Join(base, "cache", "warte")},
				{p.StateDir, filepath.Join(base, "state", "warte")},
				{p.LogFile, filepath.Join(base, "state", "warte", "warte.log")},
			}
			for _, tt := range tests {
				if tt.got != tt.want {
					t.Errorf("got %q, want %q", tt.got, tt.want)
				}
			}
		})
	}
}

func TestPathsXDGRelativeIgnored(t *testing.T) {
	setOS(t, "linux")
	clearXDG(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/cfg")
	d, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "warte"); d != want {
		t.Errorf("ConfigDir = %q, want %q", d, want)
	}
}

func TestPathsDarwin(t *testing.T) {
	setOS(t, "darwin")
	clearXDG(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	appSupport := filepath.Join(home, "Library", "Application Support", "warte")

	d, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if d != appSupport {
		t.Errorf("ConfigDir without ~/.config/warte = %q, want %q", d, appSupport)
	}
	if dd, _ := DataDir(); dd != appSupport {
		t.Errorf("DataDir = %q", dd)
	}
	if cd, _ := CacheDir(); cd != filepath.Join(home, "Library", "Caches", "warte") {
		t.Errorf("CacheDir = %q", cd)
	}
	if sd, _ := StateDir(); sd != filepath.Join(home, "Library", "Logs", "warte") {
		t.Errorf("StateDir = %q", sd)
	}

	dot := filepath.Join(home, ".config", "warte")
	if err := os.MkdirAll(dot, 0o700); err != nil {
		t.Fatal(err)
	}
	if d, _ := ConfigDir(); d != dot {
		t.Errorf("ConfigDir with ~/.config/warte = %q, want %q", d, dot)
	}
}

func TestConfigPathEnv(t *testing.T) {
	setOS(t, "linux")
	clearXDG(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(EnvConfig, "/etc/warte.toml")
	p, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != "/etc/warte.toml" {
		t.Errorf("ConfigPath = %q", p)
	}
}

func TestEnsureDirs(t *testing.T) {
	setOS(t, "linux")
	clearXDG(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Pre-create one directory with loose permissions; it must be tightened.
	loose := filepath.Join(home, ".cache", "warte")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := EnsureDirs()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p.ConfigDir, p.DataDir, p.CacheDir, p.StateDir} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s perm = %o, want 700", d, perm)
		}
	}

	// Idempotent.
	if _, err := EnsureDirs(); err != nil {
		t.Errorf("second EnsureDirs: %v", err)
	}
}

func TestEnsureDirsCustomConfigParent(t *testing.T) {
	setOS(t, "linux")
	clearXDG(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	custom := filepath.Join(home, "elsewhere", "w.toml")
	t.Setenv(EnvConfig, custom)
	if _, err := EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(custom)); err != nil || !fi.IsDir() {
		t.Errorf("parent of WARTE_CONFIG not created: %v", err)
	}
}

func TestEnsureDirsNotADirectory(t *testing.T) {
	setOS(t, "linux")
	clearXDG(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".cache", "warte"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDirs(); err == nil {
		t.Error("EnsureDirs succeeded although cache dir is a file")
	}
}
