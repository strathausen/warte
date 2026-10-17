# warte TUI

This document describes the Bubble Tea frontend in `internal/tui` and the
extension point feature views use: `internal/tui/ui`.

```
internal/tui            App (root tea.Model), layout, chat list, conversation,
                        composer, command line, search, images, mouse, built-in overlays
internal/tui/ui         shared contract: Overlay, Context, Images, messages, registries
internal/tui/views/...  feature views (pickers, viewers, settings, …) — one package each
internal/tui/keymap     modal key engine (bindings, counts, leader, help data)
internal/tui/theme      palettes, Styles, icons, sanitizing
```

Views import `ui` (plus `keymap`, `theme`, `format`, `termimg`, `core`), never
`tui`. `tui` never imports views; `cmd/warte` blank-imports `internal/tui/views/all`,
which imports every view package (their `init` functions register commands
and action handlers) and wires the shell hooks described in
[Feature integration](#feature-integration).

## The App in one page

* `tui.New(svc, cfg, tui.Options{})` returns `*tui.App`, a `tea.Model`. Run it
  with `tea.NewProgram(app)`; alt screen, focus reporting, mouse mode,
  window title and terminal background are set through `tea.View` fields.
* **Event pump**: `Init` starts a command that blocks on `svc.Events()` and
  is re-armed after every event. Every `core.Event` is handled by the App
  (`events.go`) and then forwarded to the top overlay as `ui.EventMsg`.
* **Rendering**: `Update` renders the frame right away (so image requests
  discovered while rendering become commands) and `View` returns it. Every
  frame line is exactly the terminal width; tests assert this for every
  theme, both layouts and several sizes.
* **Layout**: three rounded panes — chat list (`chat_list_width`),
  conversation, optional sidebar (`<leader>i`) — plus a one-line status bar.
  Below 90 columns only one pane is shown (phone-style: `enter` opens the
  chat, `q`/`H`/`esc` go back).
* **Modes** are owned by the keymap engine; the App switches them
  (`i` → Insert, `v`/`V` → Visual, `:` → Command, `/` → Search). Overlays always
  run in Normal mode; the previous mode is restored when the last overlay
  closes. In Insert mode `←` at the very start of the composer text
  (`composer.left`) leaves Insert mode for the chat list, keeping the draft;
  `↑` on its first line (`composer.up`) selects the newest message, and in
  Normal mode `↓` on the newest message (`message.down`) returns to Insert.
* **Conversation** is virtualized: messages are rendered per block
  (dividers + bubble + reactions) and cached by message id, revision, width
  and flags; only blocks around the viewport are rendered. Older pages load
  lazily (`svc.Messages`) when the top comes into view; `gg` loads everything.
* **Images** (`images.go`): protocol detected once (`termimg.Detect` +
  `images` config). Kitty uses Unicode placeholders, so thumbnails are cells in
  the frame and the pixels are sent out of band with `tea.Raw`, once per
  (image, size), with ids bounded by `termimg.Allocator`. **iTerm2 and sixel
  are overlay protocols and currently fall back to half-blocks inside the
  scrolling list.** Inside tmux, kitty placeholders need
  `set -g allow-passthrough on`; with `images = "auto"` warte asks tmux and
  falls back to half-blocks when passthrough is off. Fetch/decode/resize
  happen in commands on a 4-worker semaphore; blurhash placeholders are
  shown meanwhile. Bubbles with a photo or video are as wide as the
  thumbnail; the caption wraps to it.

## Writing a view (overlay)

An overlay is a floating dialog drawn centered over the dimmed main screen.
The App draws the rounded border, the title (`Title()`), the footer (key hints)
and a drop shadow; your `View(width, height)` only fills the content area.

```go
type Overlay interface {
	Init() tea.Cmd
	Update(tea.Msg) (Overlay, tea.Cmd) // return nil to close
	View(width, height int) string     // at most width cells × height lines
	Title() string
	KeyContext() string                // keymap context: keymap.Overlay(KeyContext())
	Size(maxW, maxH int) (w, h int)    // preferred content size; clamped by the App
}
```

Optional interfaces:

| Interface | Purpose |
| --- | --- |
| `ui.BackHandler` | `HandleBack() (handled bool, cmd)` intercepts `q`/`esc` (e.g. clear a filter first). |
| `ui.KeyCapturer` | `CaptureKeys() bool` — while true, every key press arrives raw (`tea.KeyPressMsg`), bypassing the keymap. Use it while a text input has focus; handle `esc` yourself. |
| `ui.Footer` | `Footer() string` replaces the default "esc close" hint in the bottom border. |

### Input

* Keys are fed to the keymap engine in context `keymap.Overlay(KeyContext())`.
  Lookup falls back to `keymap.AnyOverlay` (q/esc → `ActBack`, enter →
  `ActItemSelect`) and then to Normal-mode globals (j/k, gg/G, ctrl+d …).
  A fired binding arrives as `ui.ActionMsg{Action, Count, Keys}`; unbound keys
  arrive as the original `tea.KeyPressMsg`.
* `keymap.ActBack` closes the overlay unless your `HandleBack` handles it.
* Numeric keys are count prefixes in Normal mode; if you need them as
  shortcuts (like the reaction bar's 1–6), implement `KeyCapturer`.
* Mouse events inside your content area arrive with coordinates relative to
  its top-left corner (clicks outside close the overlay).
* Other messages: results of your own commands, `ui.EventMsg` (backend
  events), `ui.ImageReadyMsg` (an image you asked for finished loading).

### Talking to the App

Return these messages from commands (helpers in parentheses):

| Message | Effect |
| --- | --- |
| `ui.CloseOverlayMsg` (`ui.CloseOverlay`) | pop the top overlay (or return `nil` from `Update`) |
| `ui.PushOverlayMsg` (`ui.Push(o)`) | push another overlay |
| `ui.ToastMsg` (`ui.Toast(level, text)`) | transient notification, top right |
| `ui.OpenChatMsg{Chat, Focus}` | open a chat, optionally jump to a message (history loads as needed) |
| `ui.ReplyToMsg{ID}` | start a reply in the composer (Insert mode) |
| `ui.ComposeInsertMsg{Text}` | insert text at the composer cursor (Insert mode) |
| `ui.RunCommandMsg{Line}` | run an ex command line, e.g. `"mute 8h"` |
| `ui.ReactMsg{ID, Emoji, Toggle}` | react (Toggle removes an existing identical reaction) |

### Context

Constructors receive a `ui.Context`:

```go
type Context struct {
	Svc      core.Service
	Styles   *theme.Styles   // updated in place on :theme — always read through it
	Icons    theme.IconSet
	Theme    theme.Theme     // theme at creation; prefer Styles.Theme
	Config   config.Config
	Images   ui.Images
	Keys     *keymap.Engine
	Now      func() time.Time
	Chat     func() core.ChatID      // chat open in the conversation ("" if none)
	Selected func() *core.Message    // selected message, or nil
	Self     core.Contact
}
```

`ui.Images` gives access to the image pipeline:

```go
cols, rows := ctx.Images.Size(ref, maxCols, maxRows)
lines, ready := ctx.Images.Rows(ref, cols, rows) // exactly rows × cols cells
```

`Rows` returns a placeholder (blurhash or styled box) until the image is ready
and schedules the fetch itself; the App re-renders (and sends
`ui.ImageReadyMsg`) when it arrives. `ui.ImageRef` selects the source:
`ImageAttachment` (message + attachment), `ImageAvatar` (avatar id) or
`ImageSticker` (pack + sticker id). Put pixel dimensions in
`ref.Attachment.Width/Height` when known so `Size` can keep the aspect ratio.

### Registering commands and actions

```go
func init() {
	ui.RegisterCommand(ui.Command{
		Name: "contacts", Help: "Browse contacts",
		Complete: nil, // func(ctx ui.Context, args []string) []string
		Run: func(ctx ui.Context, args []string) tea.Cmd {
			return ui.Push(newContactsOverlay(ctx))
		},
	})
	ui.RegisterAction(keymap.ActChatNew, func(ctx ui.Context) tea.Cmd {
		return ui.Push(newContactsOverlay(ctx))
	})
}
```

Actions the App does not implement itself are dispatched to registered
handlers: `finder.chats` (`<leader><leader>`, built-in fallback: a simple chat
picker), `app.stories`, `app.calls`, `app.settings`, `app.theme` (fallback:
`:theme `), `chat.info`/`chat.media` (fallback: the sidebar), `chat.new`
(fallback: `:new `), `group.new`, `search.global`, `app.lock`, and
`ui.ActReactionPicker` (the "…" entry of the quick reaction bar; read the
target from `ctx.Selected()` and answer with `ui.ReactMsg`). Enter on an image
attachment dispatches `"viewer.image"` if registered (fallback: a simple
built-in image overlay).

### Example overlay

The full, compiling version lives in `internal/tui/ui/example_test.go`.

```go
type contactsOverlay struct {
	ctx      ui.Context
	contacts []core.Contact
	sel      int
}

type contactsLoaded struct{ list []core.Contact }

func (o *contactsOverlay) Init() tea.Cmd {
	svc := o.ctx.Svc
	return func() tea.Msg {
		list, _ := svc.Contacts(context.Background())
		return contactsLoaded{list}
	}
}

func (o *contactsOverlay) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case contactsLoaded:
		o.contacts = msg.list
	case ui.ActionMsg:
		switch msg.Action {
		case keymap.ActCursorDown:
			o.sel = min(o.sel+msg.Count, max(len(o.contacts)-1, 0))
		case keymap.ActCursorUp:
			o.sel = max(o.sel-msg.Count, 0)
		case keymap.ActItemSelect:
			chat := core.DirectChatID(o.contacts[o.sel].ACI)
			return nil, func() tea.Msg { return ui.OpenChatMsg{Chat: chat} }
		}
	}
	return o, nil
}

func (o *contactsOverlay) View(w, h int) string { /* render with o.ctx.Styles */ }
func (o *contactsOverlay) Title() string        { return "Contacts" }
func (o *contactsOverlay) KeyContext() string   { return "contacts" }
func (o *contactsOverlay) Size(w, h int) (int, int) { return min(40, w), min(20, h) }
```

### Rules for views

* Use `ctx.Styles` for every color; never hard-code colors.
* Sanitize untrusted text (names, bodies, file names) with
  `theme.SanitizeLine` / `theme.Sanitize` or render bodies via
  `format.Spans` + `format.Render` (which sanitize).
* Never write to stdout; emit raw terminal sequences with `tea.Raw`.
* Keep `View` fast and side-effect free apart from `Images.Rows`; do backend
  calls in commands.
* Lines must not exceed the given width (measure with `lipgloss.Width` /
  `ansi.StringWidth`); the App truncates, but truncation loses your layout.

## Built-in overlays

`?` help (grouped bindings of the current mode and pane, `/` filters),
confirm dialogs (`y`/`n`), the quick reaction bar (`R`/`+`: 1–6, h/l, enter;
"…" dispatches `ui.ActReactionPicker`; it pops up right below the selected
message instead of centered), a fuzzy chat picker (forward `f`,
`:open`, `<leader><leader>` fallback), message details (`K`) and a simple
image / view-once viewer (enter). The forward picker, message details and
viewers are fallbacks: with `views/all` linked in, the registered views
replace them. `?` over an open overlay lists that overlay's keys.

## Commands

`:q`, `:open <chat>`, `:new <phone|username|name>`, `:archive`, `:unarchive`,
`:mute [duration|off]`, `:unmute`, `:pin`, `:unpin` (the selected message in a
conversation, the chat in the chat list), `:disappear <duration|off>`,
`:block`, `:unblock`, `:accept`, `:report`, `:delete` (chat), `:theme <name>`,
`:set mouse|nomouse|mouse!|layout=bubbles|layout=irc|[no]reducemotion|[no]sidebar`,
`:attach <path>`, `:search <query>` (with filters such as `from:bob has:image
before:2026-01` it opens the message search view limited to this chat, or
to the chats named with `in:`), `:lock`, `:react <emoji|:code:>`, `:reply`, `:edit`,
`:deletemsg [!]`, `:read`, `:unread`, `:info`, `:folder <name>`, `:sidebar`,
`:w` (save changed settings), `:wq`, `:help`. View packages add more (see
the table under Feature integration); `?` lists them all with their arguments.
Tab completes command names and arguments (chat names, themes, durations,
paths — arguments with spaces such as `:open note to<tab>` complete as a
whole); repeated tabs cycle through a wildmenu shown after the input;
up/down walk the history.

## Screen lock

`:lock`, `<leader>l` (also over an overlay) and the idle timer
(`[screen_lock] enabled`, `idle_minutes`; checked every 15 seconds against
the last key, paste or mouse event) put the App into the locked state
(`lock.go`). While locked:

* `render` draws only the lock screen (logo, clock, passphrase field); the
  panes, overlays (which stay open underneath), toasts and the completion
  popup are not drawn, and new toasts are dropped;
* every key goes to the passphrase field (`ctrl+c` twice quits); mouse
  events are ignored, pastes go into the field;
* kitty images are deleted (`imageManager.hideAll` resets the allocator and
  bumps a generation, so loads that were in flight are not transmitted)
  and are sent again after unlocking;
* the window title is just "warte", notifications are reduced to "warte /
  New message" (or "Incoming call"), and nothing is marked read;
* the top overlay gets `ui.ScreenLockMsg`: the audio player pauses and the
  story viewer stops auto-advancing (so no story is marked viewed). They
  stay paused after unlocking.

The passphrase is checked by `Options.LockVerifier` (`tui.Verifier`,
`Verify(passphrase string) bool`, run in a command). `cmd/warte` passes
one that checks the data passphrase (`store.CheckPassphrase`) when the
local data is passphrase-protected, and none with keychain keys or in
`--demo`. Without one the App
uses its own lock password: the first `:lock` asks for a new password twice
and stores an argon2id hash (`argon2id$v=19$t=…,m=…,p=…$salt$hash`) in
`Options.LockPath` (default: `lock` next to `config.toml`, mode 0600). The
idle lock never engages before a password exists (a warning toast says so
at startup). Wrong attempts back off 1s, 2s, 4s, … up to 5 minutes.

## Jump list

`gg`, `G`, `gq`, search hits (`n`/`N`, `/`) and switching chats record the
previously selected message; `ctrl+o` goes back and `ctrl+i` forward again,
across chats (the other chat is opened and history loaded as needed).

## Feature integration

`internal/tui/views/all` is the one place that connects the view packages to
the shell. Besides the registrations each view package does itself, it uses
these hooks from `ui` (`ui/hooks.go`):

| Hook | Used for |
| --- | --- |
| `ui.RegisterSidebar(func(Context) Pane)` | the right pane is `info.Sidebar` (built-in fallback: a static info pane) |
| `ui.RegisterPinnedStrip` | one-line pinned strip under the conversation header (`info.RenderPinnedStrip`); click opens the pinned list |
| `ui.RegisterForwarder` | `f` / visual `f` open the multi-chat `finder.NewForwardPicker` |
| `ui.OnChatOpened` | `finder.NoteRecent` (switcher recents follow every chat switch) |
| `ui.OnShutdown` | `media.StopAll` (voice notes stop when warte quits) |
| `ui.Dismisser` | overlays closed from outside (click outside, logout, quit) clean up: pickers cancel, the view-once viewer marks the message viewed, the player stops |
| `ui.ConfigChangedMsg` | the settings form applies theme, layout, mouse, reduce motion, notifications, time format live (notification content is the local `notifications` setting only; the form does not edit Signal's account-level value) |
| `ui.PreviewThemeMsg` | the theme picker re-themes the whole app while you move; cancel restores |
| `ui.RelinkMsg` | the logged-out screen quits the App; `cmd/warte` runs the link flow and restarts |
| actions `ui.ActViewImage`, `ui.ActViewOnce`, `ui.ActPlayAudio`, `ui.ActLoggedOut` | enter on a photo / view-once / voice note; logout screen |

### Reaching every feature

| Feature | Keys | Command |
| --- | --- | --- |
| Chat switcher | `<leader><leader>` | `:chats [q]`, `:open <chat>` |
| Global search | `<leader>/` | `:find <query>` |
| Theme picker (live preview) | `<leader>t` | `:themes`, `:theme <name>` |
| Help (also over any overlay) | `?` | `:help` |
| Forward (multi-chat) | `f`, visual `f` | `:forward` |
| Quick reactions → full picker | `R` / `+`, then `7` or `…` | `:react <emoji>` |
| Reaction details | `gr` | `:reactions` |
| Emoji picker (composer) | insert `ctrl+g` | `:emoji [q]` |
| Sticker picker | insert `ctrl+s` | `:sticker` |
| Image / video viewer | `enter` on media (`o` opens externally) | `:view` |
| View-once | `enter` | |
| Voice note / audio player | `enter` | `:play` |
| Media gallery | `gm` | `:media [media\|files\|links\|voice]` |
| Stories (ring in the chat list title) | `<leader>s`, click the ring | `:story [view\|new\|post <path>]` |
| New chat / new group | `<leader>n` / `<leader>g` | `:new`, `:group create` |
| Chat / group settings | `gi` | `:settings chat`, `:chatsettings`, `:group settings` |
| Safety number | sidebar action | `:verify` |
| Profile | | `:profile` |
| Settings | `<leader>,` | `:settings` |
| Screen lock | `<leader>l` | `:lock` |
| Sidebar | `<leader>i`; focus `ctrl+w l` / `L`, leave `ctrl+w h` / `H` | `:sidebar`, `:set sidebar` |
| Message details | `K` | `:info`, `:details` |
| Call log | `<leader>c` | `:calls` |
| Pinned messages | `gp`, click the strip | `:pins` |
| Logged out / relink | shown automatically; `enter` relinks | `:connection` |
| Polls: create / vote / end | `enter` on a poll votes | `:poll`, `:vote [n …]`, `:endpoll` |
| View-once photo or video | after `:attach` | `:viewonce` |
| Join group, install sticker pack, open contact (Signal links) | `gx` / `enter` on the link | `:join <link>`, `:stickerpack <link>`, `:new <signal.me link>` |
| Export | | `:export <path>` |

### Settings and config.toml

Changes made in `:settings`, `:theme` and `:set` apply immediately but are
not written to disk. `:w` (or `ctrl+s` inside the settings form) writes
the changed keys (`theme`, `layout`, `mouse`, `reduce_motion`,
`notifications`, `time_format`, `images`) to `config.toml` (the
`--config` path, else the XDG path). Only those top-level lines are
replaced or added; comments, other keys and tables stay as they are.
`:wq` saves and quits. Account settings (read receipts, typing indicators,
…) are synced through the Service as you change them.
