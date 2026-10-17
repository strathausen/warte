package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// clearEnv neutralizes every environment variable that influences Load.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{EnvConfig, EnvTheme, EnvImages, EnvNoColor} {
		t.Setenv(k, "")
	}
}

func hasWarning(ws []Warning, key, substr string) bool {
	for _, w := range ws {
		if w.Key == key && strings.Contains(w.Message, substr) {
			return true
		}
	}
	return false
}

func TestDefault(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	d := Default()
	checks := []struct {
		name      string
		got, want any
	}{
		{"theme", d.Theme, "auto"},
		{"layout", d.Layout, LayoutBubbles},
		{"mouse", d.Mouse, true},
		{"leader", d.Leader, " "},
		{"images", d.Images, ImagesAuto},
		{"image_max_rows", d.ImageMaxRows, 12},
		{"notifications", d.Notifications, NotifyNameAndMessage},
		{"notify_method", d.NotifyMethod, MethodAuto},
		{"editor", d.Editor, "vi"},
		{"nerd_fonts", d.NerdFonts, NerdFontsAuto},
		{"time_format", d.TimeFormat, Time24h},
		{"show_avatars", d.ShowAvatars, true},
		{"send_on_enter", d.SendOnEnter, true},
		{"screen_lock.idle_minutes", d.ScreenLock.IdleMinutes, 10},
		{"device_name", d.DeviceName, "warte"},
		{"log_level", d.LogLevel, LogInfo},
		{"log_sensitive", d.LogSensitive, false},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
		}
	}
	if len(d.Keys) != 0 || d.Keys == nil {
		t.Errorf("Keys = %#v, want empty non-nil map", d.Keys)
	}
	// Default must itself be valid.
	if ws := d.validate(); len(ws) != 0 {
		t.Errorf("Default().validate() warnings: %v", ws)
	}
}

func TestDefaultEditor(t *testing.T) {
	tests := []struct {
		visual, editor, want string
	}{
		{"", "", "vi"},
		{"", "nano", "nano"},
		{"nvim", "nano", "nvim"},
		{"  ", "emacs", "emacs"},
	}
	for _, tt := range tests {
		t.Setenv("VISUAL", tt.visual)
		t.Setenv("EDITOR", tt.editor)
		if got := Default().Editor; got != tt.want {
			t.Errorf("VISUAL=%q EDITOR=%q: editor = %q, want %q", tt.visual, tt.editor, got, tt.want)
		}
	}
}

func TestDefaultOpener(t *testing.T) {
	old := goos
	t.Cleanup(func() { goos = old })
	for name, want := range map[string]string{"darwin": "open", "linux": "xdg-open", "freebsd": "xdg-open"} {
		goos = name
		if got := Default().OpenWith[OpenDefault]; got != want {
			t.Errorf("%s: opener = %q, want %q", name, got, want)
		}
	}
}

func TestSampleRoundTrip(t *testing.T) {
	clearEnv(t)
	cfg, ws, err := Parse(Sample())
	if err != nil {
		t.Fatalf("Parse(sample): %v", err)
	}
	if len(ws) != 0 {
		t.Errorf("sample produced warnings: %v", ws)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("sample != Default()\n got: %#v\nwant: %#v", cfg, Default())
	}

	// Through the filesystem as well.
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := WriteDefault(path); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("sample perm = %o, want 600", perm)
	}
	loaded, ws, err := Load(path)
	if err != nil || len(ws) != 0 {
		t.Fatalf("Load(sample) err=%v warnings=%v", err, ws)
	}
	if !reflect.DeepEqual(loaded, Default()) {
		t.Errorf("loaded sample != Default()")
	}
	if err := WriteDefault(path); !errors.Is(err, fs.ErrExist) {
		t.Errorf("second WriteDefault err = %v, want ErrExist", err)
	}
}

// TestSampleUncommented checks that the commented-out examples in the sample
// are valid too, so users can simply uncomment them.
func TestSampleUncommented(t *testing.T) {
	var b strings.Builder
	for _, line := range strings.Split(string(Sample()), "\n") {
		if s, ok := strings.CutPrefix(line, "# "); ok && (strings.Contains(s, " = ") || strings.HasPrefix(s, "[")) {
			line = s
		}
		b.WriteString(line + "\n")
	}
	cfg, ws, err := Parse([]byte(b.String()))
	if err != nil {
		t.Fatalf("parse uncommented sample: %v\n%s", err, b.String())
	}
	for _, w := range ws {
		t.Errorf("unexpected warning: %v", w)
	}
	if cfg.Editor != "nvim" || cfg.Opener(OpenVideo) != "mpv" || len(cfg.Folders) != 1 || cfg.Keys[ModeNormal]["q"] != "" {
		t.Errorf("uncommented sample not applied: %+v", cfg)
	}
}

func TestLoadMissingFile(t *testing.T) {
	clearEnv(t)
	cfg, ws, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(ws) != 0 {
		t.Errorf("warnings: %v", ws)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("missing file did not yield defaults")
	}
}

func TestLoadErrors(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	if _, _, err := Load(dir); err == nil {
		t.Error("Load(directory) succeeded")
	}
	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("theme = \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(bad)
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("malformed TOML err = %v, want position", err)
	}
	typeErr := filepath.Join(dir, "type.toml")
	if err := os.WriteFile(typeErr, []byte("mouse = \"yes\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(typeErr); err == nil {
		t.Error("wrong TOML type accepted")
	}
}

func TestLoadUsesWarteConfig(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "custom.toml")
	if err := os.WriteFile(path, []byte(`layout = "irc"`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvConfig, path)
	cfg, _, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Layout != LayoutIRC {
		t.Errorf("layout = %q, want irc (WARTE_CONFIG ignored?)", cfg.Layout)
	}
}

func TestUnknownKeys(t *testing.T) {
	cfg, ws, err := Parse([]byte(`
theme = "nord"
colour = "red"
spell_check = true
[screen_lock]
enabled = true
pin = 1234
[extras]
x = 1
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"colour", "spell_check", "screen_lock.pin", "extras", "extras.x"} {
		if !hasWarning(ws, k, "unknown key") {
			t.Errorf("no unknown-key warning for %q; got %v", k, ws)
		}
	}
	if cfg.Theme != "nord" || !cfg.ScreenLock.Enabled {
		t.Errorf("known keys not applied: %+v", cfg)
	}
}

func TestInvalidEnums(t *testing.T) {
	d := Default()
	tests := []struct {
		toml string
		key  string
		get  func(Config) any
		want any
	}{
		{`layout = "grid"`, "layout", func(c Config) any { return c.Layout }, d.Layout},
		{`images = "png"`, "images", func(c Config) any { return c.Images }, d.Images},
		{`notifications = "all"`, "notifications", func(c Config) any { return c.Notifications }, d.Notifications},
		{`notify_method = "dbus"`, "notify_method", func(c Config) any { return c.NotifyMethod }, d.NotifyMethod},
		{`time_format = "13h"`, "time_format", func(c Config) any { return c.TimeFormat }, d.TimeFormat},
		{`log_level = "trace"`, "log_level", func(c Config) any { return c.LogLevel }, d.LogLevel},
		{`nerd_fonts = "maybe"`, "nerd_fonts", func(c Config) any { return c.NerdFonts }, NerdFontsAuto},
		{`nerd_fonts = 3`, "nerd_fonts", func(c Config) any { return c.NerdFonts }, NerdFontsAuto},
		{`theme = "  "`, "theme", func(c Config) any { return c.Theme }, d.Theme},
		{`leader = ""`, "leader", func(c Config) any { return c.Leader }, d.Leader},
		{`device_name = ""`, "device_name", func(c Config) any { return c.DeviceName }, d.DeviceName},
		{`open_with = { pdf = "zathura" }`, "open_with.pdf", func(c Config) any { return c.OpenWith }, d.OpenWith},
	}
	for _, tt := range tests {
		t.Run(tt.toml, func(t *testing.T) {
			cfg, ws, err := Parse([]byte(tt.toml))
			if err != nil {
				t.Fatal(err)
			}
			if !hasWarning(ws, tt.key, "") {
				t.Errorf("no warning for %s; got %v", tt.key, ws)
			}
			if got := tt.get(cfg); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("value = %#v, want default %#v", got, tt.want)
			}
		})
	}
}

func TestEnumNormalization(t *testing.T) {
	cfg, ws, err := Parse([]byte(`
layout = " IRC "
images = "Kitty"
notifications = "name-and-message"
time_format = "12H"
log_level = "DEBUG"
`))
	if err != nil || len(ws) != 0 {
		t.Fatalf("err=%v warnings=%v", err, ws)
	}
	if cfg.Layout != LayoutIRC || cfg.Images != ImagesKitty || cfg.Notifications != NotifyNameAndMessage ||
		cfg.TimeFormat != Time12h || cfg.LogLevel != LogDebug {
		t.Errorf("not normalized: %+v", cfg)
	}
}

func TestRanges(t *testing.T) {
	d := Default()
	tests := []struct {
		toml string
		key  string
		get  func(Config) int
		want int
		warn bool
	}{
		{`image_max_rows = 0`, "image_max_rows", func(c Config) int { return c.ImageMaxRows }, d.ImageMaxRows, true},
		{`image_max_rows = 101`, "image_max_rows", func(c Config) int { return c.ImageMaxRows }, d.ImageMaxRows, true},
		{`image_max_rows = 1`, "image_max_rows", func(c Config) int { return c.ImageMaxRows }, 1, false},
		{`image_max_rows = 100`, "image_max_rows", func(c Config) int { return c.ImageMaxRows }, 100, false},
		{`sidebar_width = 15`, "sidebar_width", func(c Config) int { return c.SidebarWidth }, d.SidebarWidth, true},
		{`sidebar_width = 40`, "sidebar_width", func(c Config) int { return c.SidebarWidth }, 40, false},
		{`chat_list_width = 201`, "chat_list_width", func(c Config) int { return c.ChatListWidth }, d.ChatListWidth, true},
		{`chat_list_width = -3`, "chat_list_width", func(c Config) int { return c.ChatListWidth }, d.ChatListWidth, true},
		{"[screen_lock]\nidle_minutes = 0", "screen_lock.idle_minutes", func(c Config) int { return c.ScreenLock.IdleMinutes }, d.ScreenLock.IdleMinutes, true},
		{"[screen_lock]\nidle_minutes = 1441", "screen_lock.idle_minutes", func(c Config) int { return c.ScreenLock.IdleMinutes }, d.ScreenLock.IdleMinutes, true},
		{"[screen_lock]\nidle_minutes = 5", "screen_lock.idle_minutes", func(c Config) int { return c.ScreenLock.IdleMinutes }, 5, false},
	}
	for _, tt := range tests {
		t.Run(tt.toml, func(t *testing.T) {
			cfg, ws, err := Parse([]byte(tt.toml))
			if err != nil {
				t.Fatal(err)
			}
			if got := tt.get(cfg); got != tt.want {
				t.Errorf("value = %d, want %d", got, tt.want)
			}
			if hasWarning(ws, tt.key, "out of range") != tt.warn {
				t.Errorf("warning presence = %v, want %v (%v)", !tt.warn, tt.warn, ws)
			}
		})
	}
}

func TestDeviceNameTruncated(t *testing.T) {
	cfg, ws, err := Parse([]byte(`device_name = "` + strings.Repeat("ä", 60) + `"`))
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(cfg.DeviceName)); n != MaxDeviceNameLen {
		t.Errorf("len = %d, want %d", n, MaxDeviceNameLen)
	}
	if !hasWarning(ws, "device_name", "truncated") {
		t.Errorf("no truncation warning: %v", ws)
	}
}

func TestNerdFonts(t *testing.T) {
	tests := []struct {
		toml string
		want NerdFonts
	}{
		{`nerd_fonts = true`, NerdFontsOn},
		{`nerd_fonts = false`, NerdFontsOff},
		{`nerd_fonts = "auto"`, NerdFontsAuto},
		{`nerd_fonts = "Yes"`, NerdFontsOn},
		{`nerd_fonts = "off"`, NerdFontsOff},
		{`nerd_fonts = "0"`, NerdFontsOff},
		{`nerd_fonts = ""`, NerdFontsAuto},
	}
	for _, tt := range tests {
		cfg, ws, err := Parse([]byte(tt.toml))
		if err != nil || len(ws) != 0 {
			t.Errorf("%s: err=%v warnings=%v", tt.toml, err, ws)
			continue
		}
		if cfg.NerdFonts != tt.want {
			t.Errorf("%s: got %q, want %q", tt.toml, cfg.NerdFonts, tt.want)
		}
	}
	enabled := []struct {
		n        NerdFonts
		detected bool
		want     bool
	}{
		{NerdFontsAuto, true, true},
		{NerdFontsAuto, false, false},
		{NerdFontsOn, false, true},
		{NerdFontsOff, true, false},
	}
	for _, e := range enabled {
		if got := e.n.Enabled(e.detected); got != e.want {
			t.Errorf("%q.Enabled(%v) = %v, want %v", e.n, e.detected, got, e.want)
		}
	}
}

func TestOpenWith(t *testing.T) {
	old := goos
	goos = "linux"
	t.Cleanup(func() { goos = old })

	cfg, ws, err := Parse([]byte(`
[open_with]
video = "mpv"
url = "  "
`))
	if err != nil || len(ws) != 0 {
		t.Fatalf("err=%v warnings=%v", err, ws)
	}
	tests := []struct{ kind, want string }{
		{OpenVideo, "mpv"},
		{OpenURL, "xdg-open"},
		{OpenImage, "xdg-open"},
		{OpenDefault, "xdg-open"},
		{"bogus", "xdg-open"},
	}
	for _, tt := range tests {
		if got := cfg.Opener(tt.kind); got != tt.want {
			t.Errorf("Opener(%q) = %q, want %q", tt.kind, got, tt.want)
		}
	}

	cfg, _, _ = Parse([]byte(`open_with = { default = "handlr open", audio = "mpv --no-video" }`))
	if cfg.Opener(OpenFile) != "handlr open" || cfg.Opener(OpenAudio) != "mpv --no-video" {
		t.Errorf("custom default not used: %v", cfg.OpenWith)
	}
	if got := (Config{}).Opener(OpenVideo); got != "xdg-open" {
		t.Errorf("zero Config opener = %q", got)
	}
}

func TestEditorOverride(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "nano")
	cfg, _, _ := Parse([]byte(`editor = "hx"`))
	if cfg.Editor != "hx" {
		t.Errorf("editor = %q", cfg.Editor)
	}
	cfg, _, _ = Parse([]byte(`editor = ""`))
	if cfg.Editor != "nano" {
		t.Errorf("empty editor = %q, want env default", cfg.Editor)
	}
}

func TestApplyEnv(t *testing.T) {
	tests := []struct {
		name               string
		theme, images, nc  string
		wantTheme, wantImg string
		wantNoColor        bool
		wantWarn           bool
	}{
		{"none", "", "", "", "auto", "auto", false, false},
		{"theme", "dracula", "", "", "dracula", "auto", false, false},
		{"images", "", "Sixel", "", "auto", "sixel", false, false},
		{"bad images", "", "png", "", "auto", "auto", false, true},
		{"no color", "", "", "1", "auto", "auto", true, false},
		{"all", "nord", "off", "yes", "nord", "off", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvTheme, tt.theme)
			t.Setenv(EnvImages, tt.images)
			t.Setenv(EnvNoColor, tt.nc)
			t.Setenv(EnvConfig, "")
			cfg, ws, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Theme != tt.wantTheme || cfg.Images != tt.wantImg || cfg.NoColor != tt.wantNoColor {
				t.Errorf("got theme=%q images=%q nocolor=%v", cfg.Theme, cfg.Images, cfg.NoColor)
			}
			if got := hasWarning(ws, "$"+EnvImages, "invalid"); got != tt.wantWarn {
				t.Errorf("warning = %v, want %v (%v)", got, tt.wantWarn, ws)
			}
		})
	}
}

func TestEnvOverridesFile(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(path, []byte("theme = \"light\"\nimages = \"kitty\"\nno_color = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvTheme, "dark")
	t.Setenv(EnvImages, "blocks")
	t.Setenv(EnvNoColor, "1")
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "dark" || cfg.Images != ImagesBlocks || !cfg.NoColor {
		t.Errorf("env did not override file: %+v", cfg)
	}
}

func TestWarningString(t *testing.T) {
	if s := (Warning{Key: "a.b", Message: "bad"}).String(); s != "a.b: bad" {
		t.Errorf("got %q", s)
	}
	if s := (Warning{Message: "bad"}).String(); s != "bad" {
		t.Errorf("got %q", s)
	}
}
