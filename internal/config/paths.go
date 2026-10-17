package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AppName is the directory name used below every base directory.
const AppName = "warte"

// ConfigFileName is the name of the configuration file inside ConfigDir.
const ConfigFileName = "config.toml"

// LogFileName is the name of the log file inside StateDir.
const LogFileName = "warte.log"

// EnvConfig names the environment variable that overrides the config file path.
const EnvConfig = "WARTE_CONFIG"

// goos is the operating system used for path resolution. It is a variable so
// tests can exercise macOS and Linux behavior on any host.
var goos = runtime.GOOS

// dirMode is the permission used for every directory warte creates. All of
// them may contain private data (keys, message cache, logs).
const dirMode os.FileMode = 0o700

// Paths bundles every filesystem location warte uses.
type Paths struct {
	ConfigDir  string // configuration directory
	ConfigFile string // config.toml (or $WARTE_CONFIG)
	DataDir    string // encrypted database and attachment store
	CacheDir   string // regenerable caches (thumbnails, avatars)
	StateDir   string // logs and other non-essential state
	LogFile    string // debug log file
}

// ResolvePaths computes all locations without touching the filesystem
// (except for the existence check that ConfigDir performs on macOS).
func ResolvePaths() (Paths, error) {
	var p Paths
	var err error
	if p.ConfigDir, err = ConfigDir(); err != nil {
		return Paths{}, err
	}
	if p.ConfigFile, err = ConfigPath(); err != nil {
		return Paths{}, err
	}
	if p.DataDir, err = DataDir(); err != nil {
		return Paths{}, err
	}
	if p.CacheDir, err = CacheDir(); err != nil {
		return Paths{}, err
	}
	if p.StateDir, err = StateDir(); err != nil {
		return Paths{}, err
	}
	p.LogFile = filepath.Join(p.StateDir, LogFileName)
	return p, nil
}

// home returns the user's home directory.
func home() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: cannot determine home directory: %w", err)
	}
	return h, nil
}

// xdg returns $env/warte when env is set to an absolute path. Per the XDG
// Base Directory specification relative values are invalid and ignored.
func xdg(env string) (string, bool) {
	v := os.Getenv(env)
	if v == "" || !filepath.IsAbs(v) {
		return "", false
	}
	return filepath.Join(v, AppName), true
}

// ConfigDir returns the configuration directory.
//
// Resolution order:
//  1. $XDG_CONFIG_HOME/warte when XDG_CONFIG_HOME is set (any OS).
//  2. On macOS: ~/.config/warte if that directory already exists, otherwise
//     ~/Library/Application Support/warte. This lets dotfile users keep a
//     Linux-style layout while giving everyone else the platform default.
//  3. Elsewhere: ~/.config/warte.
func ConfigDir() (string, error) {
	if d, ok := xdg("XDG_CONFIG_HOME"); ok {
		return d, nil
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	dotConfig := filepath.Join(h, ".config", AppName)
	if goos == "darwin" {
		if fi, err := os.Stat(dotConfig); err == nil && fi.IsDir() {
			return dotConfig, nil
		}
		return filepath.Join(h, "Library", "Application Support", AppName), nil
	}
	return dotConfig, nil
}

// ConfigPath returns the config file path: $WARTE_CONFIG if set (a leading
// "~/" is expanded to the home directory), otherwise ConfigDir()/config.toml.
func ConfigPath() (string, error) {
	if p := os.Getenv(EnvConfig); p != "" {
		if rest, ok := strings.CutPrefix(p, "~/"); ok || p == "~" {
			h, err := home()
			if err != nil {
				return "", err
			}
			return filepath.Join(h, rest), nil
		}
		return p, nil
	}
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ConfigFileName), nil
}

// DataDir returns the data directory: $XDG_DATA_HOME/warte when set,
// ~/Library/Application Support/warte on macOS, ~/.local/share/warte
// elsewhere.
func DataDir() (string, error) {
	if d, ok := xdg("XDG_DATA_HOME"); ok {
		return d, nil
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	if goos == "darwin" {
		return filepath.Join(h, "Library", "Application Support", AppName), nil
	}
	return filepath.Join(h, ".local", "share", AppName), nil
}

// CacheDir returns the cache directory: $XDG_CACHE_HOME/warte when set,
// ~/Library/Caches/warte on macOS, ~/.cache/warte elsewhere.
func CacheDir() (string, error) {
	if d, ok := xdg("XDG_CACHE_HOME"); ok {
		return d, nil
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	if goos == "darwin" {
		return filepath.Join(h, "Library", "Caches", AppName), nil
	}
	return filepath.Join(h, ".cache", AppName), nil
}

// StateDir returns the state directory (logs): $XDG_STATE_HOME/warte when
// set, ~/Library/Logs/warte on macOS, ~/.local/state/warte elsewhere.
func StateDir() (string, error) {
	if d, ok := xdg("XDG_STATE_HOME"); ok {
		return d, nil
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	if goos == "darwin" {
		return filepath.Join(h, "Library", "Logs", AppName), nil
	}
	return filepath.Join(h, ".local", "state", AppName), nil
}

// LogPath returns the path of the debug log file inside StateDir.
func LogPath() (string, error) {
	d, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, LogFileName), nil
}

// EnsureDirs creates the config, data, cache and state directories with mode
// 0700 and tightens the permissions of the leaf directories if they already
// exist with looser ones. It returns the resolved paths.
func EnsureDirs() (Paths, error) {
	p, err := ResolvePaths()
	if err != nil {
		return Paths{}, err
	}
	dirs := []string{p.ConfigDir, p.DataDir, p.CacheDir, p.StateDir}
	if cf := filepath.Dir(p.ConfigFile); cf != p.ConfigDir {
		// $WARTE_CONFIG points elsewhere; make sure its parent exists but do
		// not change permissions of a directory we may not own.
		if err := os.MkdirAll(cf, dirMode); err != nil {
			return Paths{}, fmt.Errorf("config: create %s: %w", cf, err)
		}
	}
	var errs []error
	for _, d := range dirs {
		if err := ensureDir(d); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Paths{}, err
	}
	return p, nil
}

// ensureDir creates d with dirMode and restricts it to dirMode if it already
// exists with broader permissions.
func ensureDir(d string) error {
	if err := os.MkdirAll(d, dirMode); err != nil {
		return fmt.Errorf("config: create %s: %w", d, err)
	}
	fi, err := os.Stat(d)
	if err != nil {
		return fmt.Errorf("config: stat %s: %w", d, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("config: %s exists and is not a directory", d)
	}
	if fi.Mode().Perm()&^dirMode != 0 {
		if err := os.Chmod(d, dirMode); err != nil {
			return fmt.Errorf("config: chmod %s: %w", d, err)
		}
	}
	return nil
}
