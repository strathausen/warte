// Package config loads warte's TOML configuration and resolves the
// filesystem locations (config, data, cache, logs) it uses.
//
// The configuration file is optional: a missing file yields Default().
// Unknown keys and invalid values never abort startup; they produce
// Warnings and fall back to defaults. Only malformed TOML (or a value of the
// wrong TOML type) is reported as an error.
package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Layout values for Config.Layout.
const (
	LayoutBubbles = "bubbles" // chat bubbles
	LayoutIRC     = "irc"     // dense "12:41 <alice> hi" lines
)

// Image protocol values for Config.Images.
const (
	ImagesAuto   = "auto"
	ImagesKitty  = "kitty"
	ImagesITerm2 = "iterm2"
	ImagesSixel  = "sixel"
	ImagesBlocks = "blocks"
	ImagesOff    = "off"
)

// Notification privacy levels for Config.Notifications.
const (
	NotifyNameAndMessage = "name_and_message" // sender and message preview
	NotifyName           = "name"             // sender only
	NotifyNone           = "none"             // "new message" without details
	NotifyOff            = "off"              // no notifications at all
)

// Notification delivery methods for Config.NotifyMethod.
const (
	MethodAuto   = "auto"   // OSC if the terminal supports it, else native, else bell
	MethodOSC    = "osc"    // OSC 9/99/777 terminal notifications
	MethodNative = "native" // macOS notification center / libnotify
	MethodBell   = "bell"   // terminal bell only
	MethodOff    = "off"
)

// Time formats for Config.TimeFormat.
const (
	Time24h = "24h"
	Time12h = "12h"
)

// Log levels for Config.LogLevel.
const (
	LogDebug = "debug"
	LogInfo  = "info"
	LogWarn  = "warn"
	LogError = "error"
)

// OpenWith keys.
const (
	OpenDefault = "default"
	OpenVideo   = "video"
	OpenAudio   = "audio"
	OpenImage   = "image"
	OpenFile    = "file"
	OpenURL     = "url"
)

// Key map modes accepted under [keys.<mode>].
const (
	ModeNormal  = "normal"
	ModeInsert  = "insert"
	ModeVisual  = "visual"
	ModeCommand = "command"
	ModeSearch  = "search"
)

// Environment variables consulted by ApplyEnv.
const (
	EnvTheme   = "WARTE_THEME"
	EnvImages  = "WARTE_IMAGES"
	EnvNoColor = "NO_COLOR"
)

var (
	layouts       = []string{LayoutBubbles, LayoutIRC}
	imageModes    = []string{ImagesAuto, ImagesKitty, ImagesITerm2, ImagesSixel, ImagesBlocks, ImagesOff}
	notifyLevels  = []string{NotifyNameAndMessage, NotifyName, NotifyNone, NotifyOff}
	notifyMethods = []string{MethodAuto, MethodOSC, MethodNative, MethodBell, MethodOff}
	timeFormats   = []string{Time24h, Time12h}
	logLevels     = []string{LogDebug, LogInfo, LogWarn, LogError}
	openWithKeys  = []string{OpenDefault, OpenVideo, OpenAudio, OpenImage, OpenFile, OpenURL}
	keyModes      = []string{ModeNormal, ModeInsert, ModeVisual, ModeCommand, ModeSearch}
)

// Ranges for numeric settings (inclusive).
const (
	MinImageMaxRows  = 1
	MaxImageMaxRows  = 100
	MinPaneWidth     = 16
	MaxPaneWidth     = 200
	MinIdleMinutes   = 1
	MaxIdleMinutes   = 24 * 60
	MaxDeviceNameLen = 50
)

// Config is warte's user configuration. TOML keys are snake_case; the
// default of every field is documented on the field and produced by Default.
type Config struct {
	// Theme is a theme name such as "catppuccin-mocha", or "auto" to pick a
	// light or dark theme from the terminal background. Default "auto".
	// Overridden by $WARTE_THEME.
	Theme string `toml:"theme"`

	// Layout is the message layout: "bubbles" or "irc". Default "bubbles".
	Layout string `toml:"layout"`

	// Mouse enables mouse support (click to focus, wheel scrolling).
	// Default false.
	Mouse bool `toml:"mouse"`

	// Leader is the key sequence substituted for <leader> in key maps.
	// Default " " (space).
	Leader string `toml:"leader"`

	// Images selects the inline image protocol: "auto", "kitty", "iterm2",
	// "sixel", "blocks" or "off". Default "auto". Overridden by $WARTE_IMAGES.
	Images string `toml:"images"`

	// ImageMaxRows caps the height of inline images in terminal rows,
	// 1..100. Default 12.
	ImageMaxRows int `toml:"image_max_rows"`

	// Notifications is the notification privacy level: "name_and_message",
	// "name", "none" (content-free notice) or "off". Default
	// "name_and_message".
	Notifications string `toml:"notifications"`

	// NotifyMethod selects how notifications are delivered: "auto", "osc",
	// "native", "bell" or "off". Default "auto".
	NotifyMethod string `toml:"notify_method"`

	// Editor is the external editor command for composing long messages.
	// Default: $VISUAL, then $EDITOR, then "vi". An empty value in the file
	// keeps the default.
	Editor string `toml:"editor"`

	// OpenWith maps a media kind ("default", "video", "audio", "image",
	// "file", "url") to the command used to open it externally. Kinds that
	// are not set fall back to "default", which defaults to "open" on macOS
	// and "xdg-open" elsewhere. See Config.Opener.
	OpenWith map[string]string `toml:"open_with"`

	// ReduceMotion disables animations (spring scrolling, spinners become
	// static). Default false.
	ReduceMotion bool `toml:"reduce_motion"`

	// NerdFonts controls Nerd Font icon usage: "auto" (detect), true or
	// false. Strings "on"/"off"/"yes"/"no"/"true"/"false" are accepted.
	// Default "auto".
	NerdFonts NerdFonts `toml:"nerd_fonts"`

	// TimeFormat is "24h" or "12h". Default "24h".
	TimeFormat string `toml:"time_format"`

	// ShowAvatars shows avatars (images or colored initials) in the chat
	// list and conversation. Default true.
	ShowAvatars bool `toml:"show_avatars"`

	// SidebarWidth is the right sidebar width in columns, 16..200.
	// Default 36.
	SidebarWidth int `toml:"sidebar_width"`

	// ChatListWidth is the chat list width in columns, 16..200. Default 32.
	ChatListWidth int `toml:"chat_list_width"`

	// SendOnEnter sends the message on Enter in insert mode, with Alt-Enter
	// (or Shift-Enter where the terminal reports it) inserting a newline.
	// When false, Enter inserts a newline and Alt-Enter / Ctrl-Enter sends;
	// Ctrl-Enter is only distinguishable on terminals with the kitty
	// keyboard protocol, so Alt-Enter is the portable choice. Default true.
	SendOnEnter bool `toml:"send_on_enter"`

	// NoColor disables all colors. Default false; forced to true when the
	// NO_COLOR environment variable is set to a non-empty value.
	NoColor bool `toml:"no_color"`

	// ScreenLock configures the idle screen lock.
	ScreenLock ScreenLock `toml:"screen_lock"`

	// Keys holds user key map overrides: mode ("normal", "insert",
	// "visual", "command", "search") → key sequence → action name. An action
	// of "" or "none" removes the default binding. Actions starting with ":"
	// run an ex command. Default empty. See Config.KeyBindings.
	Keys map[string]map[string]string `toml:"keys"`

	// Folders defines chat folders shown as tabs (cycled with gt / gT).
	// Default none (only the implicit "All chats").
	Folders []FolderConfig `toml:"folders"`

	// DeviceName is the name this linked device registers with. Default
	// "warte". At most 50 characters.
	DeviceName string `toml:"device_name"`

	// LogLevel is "debug", "info", "warn" or "error". Default "info".
	LogLevel string `toml:"log_level"`

	// LogSensitive allows message bodies and phone numbers in logs.
	// Default false. Also enabled by the --log-sensitive flag.
	LogSensitive bool `toml:"log_sensitive"`
}

// ScreenLock configures the screen lock that blanks the UI and asks for the
// passphrase.
type ScreenLock struct {
	// Enabled turns on locking after IdleMinutes of inactivity (":lock"
	// always works). Default false.
	Enabled bool `toml:"enabled"`
	// IdleMinutes is the idle timeout, 1..1440. Default 10.
	IdleMinutes int `toml:"idle_minutes"`
}

// Default returns the built-in configuration. Editor and OpenWith depend on
// the environment ($VISUAL, $EDITOR) and the operating system.
func Default() Config {
	return Config{
		Theme:         "auto",
		Layout:        LayoutBubbles,
		Mouse:         true,
		Leader:        " ",
		Images:        ImagesAuto,
		ImageMaxRows:  12,
		Notifications: NotifyNameAndMessage,
		NotifyMethod:  MethodAuto,
		Editor:        defaultEditor(),
		OpenWith:      map[string]string{OpenDefault: defaultOpener()},
		ReduceMotion:  false,
		NerdFonts:     NerdFontsAuto,
		TimeFormat:    Time24h,
		ShowAvatars:   true,
		SidebarWidth:  36,
		ChatListWidth: 32,
		SendOnEnter:   true,
		NoColor:       false,
		ScreenLock:    ScreenLock{Enabled: false, IdleMinutes: 10},
		Keys:          map[string]map[string]string{},
		Folders:       nil,
		DeviceName:    "warte",
		LogLevel:      LogInfo,
		LogSensitive:  false,
	}
}

func defaultEditor() string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return "vi"
}

func defaultOpener() string {
	if goos == "darwin" {
		return "open"
	}
	return "xdg-open"
}

// Opener returns the command for opening media of the given kind (one of
// the OpenWith keys), falling back to OpenWith["default"] and finally to the
// platform opener.
func (c Config) Opener(kind string) string {
	if v := strings.TrimSpace(c.OpenWith[kind]); v != "" {
		return v
	}
	if v := strings.TrimSpace(c.OpenWith[OpenDefault]); v != "" {
		return v
	}
	return defaultOpener()
}

// Warning describes a non-fatal configuration problem. The offending value
// has been replaced by its default (or ignored).
type Warning struct {
	Key     string // dotted TOML key, e.g. "keys.normal" or "image_max_rows"
	Message string
}

// String formats the warning as "key: message". Control characters are
// removed, since keys and values come from the user's file or environment
// and the result is typically printed to the terminal.
func (w Warning) String() string {
	if w.Key == "" {
		return stripControl(w.Message)
	}
	return stripControl(w.Key + ": " + w.Message)
}

//go:embed sample.toml
var sample []byte

// Sample returns the fully commented sample configuration that WriteDefault
// writes. Parsing it yields Default().
func Sample() []byte { return bytes.Clone(sample) }

// WriteDefault writes the commented sample configuration to path with mode
// 0600, creating the parent directory (0700) if needed. It never overwrites
// an existing file; in that case the returned error wraps fs.ErrExist.
func WriteDefault(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("config: create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("config: write sample: %w", err)
	}
	if _, err := f.Write(sample); err != nil {
		f.Close()
		return fmt.Errorf("config: write sample: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("config: write sample: %w", err)
	}
	return nil
}

// Load reads the configuration from path, then applies environment
// overrides (see ApplyEnv). An empty path means ConfigPath() (which honors
// $WARTE_CONFIG). A missing file is not an error: the defaults are used.
// Unknown keys and invalid values produce warnings; malformed TOML is an
// error.
func Load(path string) (Config, []Warning, error) {
	if path == "" {
		p, err := ConfigPath()
		if err != nil {
			return Default(), nil, err
		}
		path = p
	}
	data, err := os.ReadFile(path)
	var cfg Config
	var warns []Warning
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cfg = Default()
	case err != nil:
		return Default(), nil, fmt.Errorf("config: read %s: %w", path, err)
	default:
		cfg, warns, err = Parse(data)
		if err != nil {
			return Default(), warns, fmt.Errorf("config: %s: %w", path, err)
		}
	}
	warns = append(warns, ApplyEnv(&cfg)...)
	return cfg, warns, nil
}

// Parse decodes TOML data on top of Default() and validates the result. It
// does not consult environment overrides.
func Parse(data []byte) (Config, []Warning, error) {
	cfg := Default()
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&cfg)
	if err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			return Default(), nil, errors.New(perr.ErrorWithPosition())
		}
		return Default(), nil, err
	}
	var warns []Warning
	for _, k := range md.Undecoded() {
		warns = append(warns, Warning{Key: k.String(), Message: "unknown key (ignored)"})
	}
	warns = append(warns, cfg.validate()...)
	return cfg, warns, nil
}

// ApplyEnv applies environment overrides to c:
//   - $WARTE_THEME sets Theme,
//   - $WARTE_IMAGES sets Images (invalid values are ignored with a warning),
//   - a non-empty $NO_COLOR sets NoColor (https://no-color.org).
//
// $WARTE_CONFIG is handled by ConfigPath.
func ApplyEnv(c *Config) []Warning {
	var warns []Warning
	if v := strings.TrimSpace(os.Getenv(EnvTheme)); v != "" {
		c.Theme = v
	}
	if v := os.Getenv(EnvImages); v != "" {
		n := normEnum(v)
		if slices.Contains(imageModes, n) {
			c.Images = n
		} else {
			warns = append(warns, Warning{Key: "$" + EnvImages, Message: fmt.Sprintf("invalid value %q (want %s); ignored", v, strings.Join(imageModes, ", "))})
		}
	}
	if os.Getenv(EnvNoColor) != "" {
		c.NoColor = true
	}
	return warns
}

func normEnum(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.ReplaceAll(s, "-", "_")
}

// validate normalizes enum strings and replaces invalid values with their
// defaults, returning a warning for each replacement.
func (c *Config) validate() []Warning {
	d := Default()
	var w []Warning
	enum := func(key string, v *string, allowed []string, def string) {
		n := normEnum(*v)
		if slices.Contains(allowed, n) {
			*v = n
			return
		}
		w = append(w, Warning{Key: key, Message: fmt.Sprintf("invalid value %q (want %s); using %q", *v, strings.Join(allowed, ", "), def)})
		*v = def
	}
	rng := func(key string, v *int, lo, hi, def int) {
		if *v < lo || *v > hi {
			w = append(w, Warning{Key: key, Message: fmt.Sprintf("value %d out of range %d..%d; using %d", *v, lo, hi, def)})
			*v = def
		}
	}

	if c.Theme = strings.TrimSpace(c.Theme); c.Theme == "" {
		w = append(w, Warning{Key: "theme", Message: fmt.Sprintf("empty value; using %q", d.Theme)})
		c.Theme = d.Theme
	}
	enum("layout", &c.Layout, layouts, d.Layout)
	if c.Leader == "" {
		w = append(w, Warning{Key: "leader", Message: fmt.Sprintf("empty value; using %q", d.Leader)})
		c.Leader = d.Leader
	}
	enum("images", &c.Images, imageModes, d.Images)
	rng("image_max_rows", &c.ImageMaxRows, MinImageMaxRows, MaxImageMaxRows, d.ImageMaxRows)
	enum("notifications", &c.Notifications, notifyLevels, d.Notifications)
	enum("notify_method", &c.NotifyMethod, notifyMethods, d.NotifyMethod)
	if c.Editor = strings.TrimSpace(c.Editor); c.Editor == "" {
		c.Editor = d.Editor
	}
	w = append(w, c.validateOpenWith(d)...)
	if !c.NerdFonts.Valid() {
		w = append(w, Warning{Key: "nerd_fonts", Message: fmt.Sprintf("invalid value %q (want auto, true, false); using %q", string(c.NerdFonts), string(NerdFontsAuto))})
		c.NerdFonts = NerdFontsAuto
	}
	enum("time_format", &c.TimeFormat, timeFormats, d.TimeFormat)
	rng("sidebar_width", &c.SidebarWidth, MinPaneWidth, MaxPaneWidth, d.SidebarWidth)
	rng("chat_list_width", &c.ChatListWidth, MinPaneWidth, MaxPaneWidth, d.ChatListWidth)
	rng("screen_lock.idle_minutes", &c.ScreenLock.IdleMinutes, MinIdleMinutes, MaxIdleMinutes, d.ScreenLock.IdleMinutes)
	w = append(w, c.validateKeys()...)
	w = append(w, c.validateFolders()...)
	c.DeviceName = strings.TrimSpace(c.DeviceName)
	if clean := strings.TrimSpace(stripControl(c.DeviceName)); clean != c.DeviceName {
		w = append(w, Warning{Key: "device_name", Message: "control characters removed"})
		c.DeviceName = clean
	}
	switch {
	case c.DeviceName == "":
		w = append(w, Warning{Key: "device_name", Message: fmt.Sprintf("empty value; using %q", d.DeviceName)})
		c.DeviceName = d.DeviceName
	case len([]rune(c.DeviceName)) > MaxDeviceNameLen:
		w = append(w, Warning{Key: "device_name", Message: fmt.Sprintf("longer than %d characters; truncated", MaxDeviceNameLen)})
		c.DeviceName = string([]rune(c.DeviceName)[:MaxDeviceNameLen])
	}
	enum("log_level", &c.LogLevel, logLevels, d.LogLevel)
	return w
}

func (c *Config) validateOpenWith(d Config) []Warning {
	var w []Warning
	if c.OpenWith == nil {
		c.OpenWith = map[string]string{}
	}
	for _, k := range sortedKeys(c.OpenWith) {
		v := strings.TrimSpace(c.OpenWith[k])
		if !slices.Contains(openWithKeys, k) {
			w = append(w, Warning{Key: "open_with." + k, Message: fmt.Sprintf("unknown kind (want %s); ignored", strings.Join(openWithKeys, ", "))})
			delete(c.OpenWith, k)
			continue
		}
		if v == "" {
			delete(c.OpenWith, k)
			continue
		}
		c.OpenWith[k] = v
	}
	if _, ok := c.OpenWith[OpenDefault]; !ok {
		c.OpenWith[OpenDefault] = d.OpenWith[OpenDefault]
	}
	return w
}

// actionRe matches action names such as "chat.mute" or "pane.focus_left".
var actionRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:[.-][a-z0-9_]+)*$`)

func (c *Config) validateKeys() []Warning {
	var w []Warning
	if c.Keys == nil {
		c.Keys = map[string]map[string]string{}
	}
	for _, mode := range sortedKeys(c.Keys) {
		m := c.Keys[mode]
		nm := strings.ToLower(strings.TrimSpace(mode))
		if !slices.Contains(keyModes, nm) {
			w = append(w, Warning{Key: "keys." + mode, Message: fmt.Sprintf("unknown mode (want %s); ignored", strings.Join(keyModes, ", "))})
			delete(c.Keys, mode)
			continue
		}
		clean := make(map[string]string, len(m))
		for _, seq := range sortedKeys(m) {
			action := strings.TrimSpace(m[seq])
			key := fmt.Sprintf("keys.%s.%q", nm, seq)
			if err := checkKeySequence(seq); err != nil {
				w = append(w, Warning{Key: key, Message: err.Error() + "; ignored"})
				continue
			}
			if strings.EqualFold(action, "none") {
				action = ""
			}
			if action != "" && !strings.HasPrefix(action, ":") && !actionRe.MatchString(action) {
				w = append(w, Warning{Key: key, Message: fmt.Sprintf("invalid action name %q; ignored", action)})
				continue
			}
			clean[seq] = action
		}
		delete(c.Keys, mode)
		if existing, ok := c.Keys[nm]; ok {
			for k, v := range clean {
				existing[k] = v
			}
		} else {
			c.Keys[nm] = clean
		}
	}
	return w
}

// checkKeySequence performs a light syntax check of a key sequence such as
// "gg", "<leader>m", "<C-w>h" or "<space><space>". Full parsing belongs to
// the keymap engine.
func checkKeySequence(seq string) error {
	if seq == "" {
		return errors.New("empty key sequence")
	}
	depth := 0
	start := 0
	for i, r := range seq {
		switch r {
		case '<':
			if depth == 0 {
				start = i
			}
			depth++
			if depth > 1 {
				return fmt.Errorf("nested '<' in key sequence %q", seq)
			}
		case '>':
			if depth == 1 {
				if i == start+1 {
					// "<>" is meaningless; treat as error.
					return fmt.Errorf("empty <> in key sequence %q", seq)
				}
				depth = 0
			}
		}
	}
	if depth != 0 {
		return fmt.Errorf("unterminated '<' in key sequence %q", seq)
	}
	return nil
}

// KeyBinding is one user key map override.
type KeyBinding struct {
	Mode   string // "normal", "insert", "visual", "command" or "search"
	Keys   string // key sequence as written, e.g. "<leader>m"
	Action string // action name, ":command", or "" when Unbind is true
	Unbind bool   // remove the default binding for Keys
}

// KeyBindings returns all key map overrides sorted by mode (in the order
// normal, insert, visual, command, search) and then by key sequence.
func (c Config) KeyBindings() []KeyBinding {
	var out []KeyBinding
	for _, mode := range keyModes {
		m := c.Keys[mode]
		for _, seq := range sortedKeys(m) {
			a := m[seq]
			if strings.EqualFold(strings.TrimSpace(a), "none") {
				a = ""
			}
			out = append(out, KeyBinding{Mode: mode, Keys: seq, Action: a, Unbind: a == ""})
		}
	}
	return out
}

// ExpandLeader replaces every "<leader>" (case-insensitive) in seq with
// the configured Leader key. A space leader is rendered as "<space>".
func (c Config) ExpandLeader(seq string) string {
	leader := c.Leader
	if leader == " " {
		leader = "<space>"
	}
	var b strings.Builder
	for {
		i := indexFoldASCII(seq, "<leader>")
		if i < 0 {
			b.WriteString(seq)
			return b.String()
		}
		b.WriteString(seq[:i])
		b.WriteString(leader)
		seq = seq[i+len("<leader>"):]
	}
}

// indexFoldASCII returns the byte index of the first ASCII case-insensitive
// match of needle (which must be ASCII) in s, or -1. Unlike
// strings.Index(strings.ToLower(s), ...) the index is valid for s itself:
// lowercasing some non-ASCII runes changes their UTF-8 length.
func indexFoldASCII(s, needle string) int {
	for i := 0; i+len(needle) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

// stripControl removes C0/C1 control characters (including ESC) so that
// user-supplied text cannot inject terminal escape sequences when shown.
func stripControl(s string) string {
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, s)
}

func isControl(r rune) bool { return r < 0x20 || (r >= 0x7f && r < 0xa0) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
