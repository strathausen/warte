# Terminal Signal Client — Spec Sheet
> Name: **warte** (chosen; see [Names](#names)). Status: draft v0.2 · 2026-10-16
## 1. Vision
A Signal client for people who live in the terminal. It should do everything Signal Desktop does, work entirely from the keyboard with vim-style keys, and look polished in the way Charm tools do. It shows images inline in terminals that support it (kitty, Ghostty, WezTerm, iTerm2, foot, …). The mouse works but is never required.

**Pillars**

1. **Parity:** if Signal Desktop can do it, this client can too. Where a terminal can't do something, it degrades gracefully instead of leaving the feature out.
  
2. **Keyboard first:** modal, vim-inspired, composable, discoverable (`?` everywhere).
  
3. **Pretty:** Charm aesthetic. Uses Lip Gloss, has themes, adapts to light and dark terminals, and has small, tasteful animations.
  
4. **Trustworthy:** uses Signal's official cryptography (libsignal), encrypts data at rest, and never leaks plaintext to temp files or scrollback.
  
## 2. What "based on the Signal client" means
Signal Desktop is TypeScript + Electron + React. Its business logic (`ts/`) is closely tied to Redux and the renderer, so "removing the GUI" from it would leave us with a fork that's painful to rebase on every release. There are three realistic foundations:

| Option | What we reuse | Pros | Cons |
| --- | --- | --- | --- |
| **A. Go + libsignal-ffi + signalmeow** _(chosen)_ | Official **libsignal** (Rust, same crypto/protocol core as all official apps) via cgo; the service layer from mautrix-signal's `signalmeow` (Go, AGPL) | Native Charm stack (Bubble Tea, Lip Gloss, Bubbles, Huh, Glamour); single static-ish binary; signalmeow is proven in production as a linked device | We have to reimplement Desktop's app logic (message state, receipts, sync) ourselves, using Desktop's code as the reference spec |
| B. Headless Signal Desktop fork (Node) + Ink/React TUI | Desktop's actual `ts/` logic | Closest to "literally the Signal client"; parity comes almost for free | Heavy Node runtime; constant merge pain against upstream; can't use Charm (Go); Ink is far less polished |
| C. Rust + presage + ratatui | libsignal natively (Rust) | One language, no FFI | Not Charm; presage is less complete; already exists in spirit (gurk-rs) |

**Decision: A.** We use Signal Desktop's source as the behavioral reference (protocol flows, edge cases, which features exist and how they act) but don't fork its code. The license is **AGPL-3.0**, matching both Signal Desktop and mautrix-signal.
## 3. Architecture
```
┌──────────────────────────── warte (single binary) ────────────────────────────┐
│  TUI (Bubble Tea v2)                                                           │
│   ├─ views: ChatList │ Conversation │ Sidebar │ Overlays (picker, help, …)     │
│   ├─ keymap engine (modes, counts, operators, leader, user remaps)             │
│   └─ render: Lip Gloss styles · Glamour-ish formatting · image layer           │
│                         ▲ msgs (tea.Cmd / tea.Msg)                             │
│  App core (UI-agnostic Go)                                                     │
│   ├─ conversation/message store, unread/receipts, drafts, search index         │
│   ├─ sync engine (contacts, groups, read state, settings from primary)         │
│   └─ media pipeline (download, decrypt, thumbnail, cache)                      │
│                         ▲                                                      │
│  Signal layer: signalmeow (websocket, envelopes, groups v2, storage service)   │
│                         ▲ cgo                                                  │
│  libsignal-ffi (Rust): Double Ratchet, sealed sender, zkgroup, SVR, backups    │
│                                                                                │
│  Storage: SQLCipher DB + encrypted attachment store; key in OS keychain        │
└────────────────────────────────────────────────────────────────────────────────┘
```

- **One process, linked device only.** warte always links to an existing Signal account on your phone, the same way Signal Desktop does. There's no standalone registration, no daemon and no attach mode. Messages that arrive while warte is closed are fetched from the server the next time it starts, just like Desktop.
  
- **CLI subcommands:** `warte link`, `warte unlink`, `warte export`. They're one-shot commands, not a scripting API.
  
### Charm stack
| Lib | Use |
| --- | --- |
| **Bubble Tea v2** | Main event loop, keyboard/mouse input, focus, resizing |
| **Lip Gloss v2** | All styling, layout, borders, adaptive light/dark colors, themes |
| **Bubbles** | viewport, textarea (composer), list, spinner, progress, help, filepicker |
| **Huh** | Onboarding (QR linking), settings forms, confirm dialogs |
| **Glamour** | Rendering long text, help pages, and the changelog |
| **Harmonica** | Spring animations (smooth scrolling, popovers, typing indicator) |
| **Log** | Debug log to a file (never to the TUI screen) |
| **x/ansi, x/term** | Low-level terminal capability detection, OSC sequences |
| _(later)_ **Wish** | Maybe: serve your own client over SSH to yourself |
## 4. Layout
```
╭─ warte ───────────────────────────────────────────────────── ● linked · ◐ 3 ─╮
│ ▸ All  Unread  Groups  Work            │ Alice Chen                  🔒  ⋮  │
│────────────────────────────────────────│────────────────────────────────────│
│ ● Alice Chen                    12:41  │            ┌──────────────┐        │
│   haha yes, send the pic               │            │   [image]    │        │
│   Book club 📚              2   11:02  │            └──────────────┘        │
│   Bob: who's bringing snacks           │            look at this    12:40 ✓✓│
│   Note to Self                  Mon    │ haha yes, send the pic             │
│   …                                    │ 12:41                   ❤️ 1       │
│                                        │────────────────────────────────────│
│                                        │ ▌message Alice…           ⏎ send   │
╰─ NORMAL ── j/k move · enter open · / search · ? help ─────────────────────────╯
```

- **Three panes:** chat list, conversation, and an optional right sidebar (contact/group info, media gallery, search results). The sidebar toggles with `<leader>i`.
  
- **Responsive:** narrow terminals collapse to one pane at a time, like a phone layout.
  
- **Status line:** current mode, connection state, sync progress, contextual key hints (Bubbles `help`).
  
## 5. Interaction model
### Modes
| Mode | Enter | Purpose |
|---|---|---|
| **Normal** | `Esc` | Navigate, act on messages |
| **Insert** | `i`, `a`, `o` | Composer |
| **Visual** | `v`, `V` | Select messages (forward, delete, copy) |
| **Command** | `:` | Ex-style commands with completion |
| **Search** | `/` | Search in chat; `<leader>/` searches everywhere (`?` is help, there is no backward search) |
### Default keymap (all remappable)
| Keys | Action |
|---|---|
| `j` / `k`, `gg` / `G`, `Ctrl-d` / `Ctrl-u` | Move / jump / half-page |
| `Ctrl-w h/l` or `H` / `L` | Switch pane focus |
| `<space><space>` | Fuzzy chat switcher (Telescope-style) |
| `]u` / `[u` | Next / previous unread chat |
| `gt` / `gT` | Next / previous chat folder |
| `Enter` | Open chat / attachment / link preview |
| `r` | Reply (quote) to the selected message |
| `R` or `+` | React (emoji picker, recent reactions first) |
| `e` | Edit own message |
| `dd` | Delete for me · `dD` delete for everyone |
| `yy` / `yl` | Copy text / copy link |
| `f` | Forward |
| `o` | Open attachment in system viewer · `gx` open URL |
| `p` | Pin / unpin message |
| `zm` / `za` / `zp` | Mute / archive / pin chat |
| `Ctrl-e` (insert) | Compose in `$EDITOR` |
| `Ctrl-a` / `Ctrl-v` (insert) | Attach a file (`:attach` with path completion) / paste text or an image from the clipboard |
| `@` (insert) | Mention completion · `:` emoji shortcode completion |
| `5j`, `3dd` | Counts work where they make sense |
| `.` | Repeat last action |
| `?` | Context help overlay |
### Command mode examples
`:new alice` · `:group create` · `:mute 8h` · `:disappear 1w` · `:block` · `:verify` · `:search from:bob has:image before:2026-01` · `:theme catppuccin-mocha` · `:set mouse` · `:export`
### Mouse (on by default, `mouse = false` to disable)
Click to focus or select, scroll wheel in any pane, click links and attachments, drag the pane divider, right-click for a context menu. On by default; hold Shift (Option in iTerm2) while dragging to use the terminal's own text selection, or toggle with `:set mouse!`.
## 6. Feature parity matrix
Target: match **Signal Desktop** as of the version current at M3.

| Area | Features | Terminal approach | Milestone | Status |
| --- | --- | --- | --- | --- |
| **Account** | Link as secondary device via QR code, unlink, multiple profiles | QR rendered in Unicode half-blocks (or a kitty image) | M0  | 🟡 Link (QR, history transfer) and `warte unlink` done. No profile switcher: a second account needs its own data dir (`XDG_DATA_HOME`) |
| **Messaging** | 1:1 and groups v2, quotes/replies, reactions, edits, delete for everyone, mentions, text formatting (bold, italic, strike, mono, spoiler), long text, Note to Self | Formatting mapped to ANSI styles; spoilers as `░░░` until revealed | M1  | ✅ |
|     | Read receipts, typing indicators, delivery status | `✓` `✓✓` symbols; animated dots for typing | M1  | ✅ |
|     | Disappearing messages, view-once | Countdown in metadata; view-once opens in a modal and is then purged | M2  | ✅ View-once is sent with `:viewonce` after `:attach` |
|     | Link previews (send and receive) | Card with thumbnail | M2  | ✅ Generated on send when the link-preview setting is on |
|     | Polls, pinned messages | Polls as inline bar charts with votes; pinned messages in a header strip | M3  | ✅ `:poll` creates a poll, Enter or `:vote` votes, `:endpoll` ends it. `p` pins, `gp` lists pins |
|     | Message requests, accept/block/report spam | Banner with actions | M1  | ✅ |
| **Media** | Images, GIFs, video, audio, files, voice notes, stickers | See §7 | M2  | ✅ |
|     | Record voice notes | Mic via PortAudio/miniaudio; waveform in braille characters | M3  | ❌ Needs a microphone library, which is not a dependency. Received voice notes play |
|     | Sticker packs: view, install, send | Sticker picker grid | M3  | ✅ Install from the picker or from a signal.art link (`gx`, `:stickerpack`) |
|     | Media gallery per chat | Sidebar grid | M3  | ✅ `gm`, `:media` (an overlay, not the sidebar) |
| **Chats** | Archive, pin, mute, mark unread, chat folders, search (local FTS) | SQLite FTS5 over decrypted content in the encrypted DB | M1–M2 | 🟡 Chat folders are read from the phone (storage service) and shown as tabs, and local folders can be set in `config.toml`. warte can't create or edit Signal folders |
|     | Drafts per chat, synced read state |     | M1  | ✅ Drafts stay on this device, as in Desktop |
| **Groups** | Create/edit, invite links, admin roles, member approval, announcement-only, group descriptions | Huh forms | M2  | ✅ Join by link with `:join` or `gx` on a signal.group link |
| **Contacts & safety** | Safety numbers, verification, blocking, usernames, phone number privacy, profile edit (name, about, avatar) | Safety number display plus QR code | M2  | 🟡 Usernames are shown and resolved, but you can't set or change your own username or username link. Nicknames are supported; contact notes aren't |
| **Stories** | View stories, reply, post text/image stories, story privacy | Full-screen story viewer, `h`/`l` to step through | M3  | 🟡 Posts to My Story and groups, with My Story's audience taken from the phone. Custom story lists and editing privacy aren't supported |
| **Calls** | Incoming call notice, call history, missed-call events in the chat | "Alice is calling, answer on your phone" banner. No call audio or video in warte | M1  | ✅ Call links are ignored |
| **Sync** | Contacts, groups, settings, storage service, history transfer at link time |     | M1–M3 | ✅ Needs the phone's storage manifest. Without it, changes stay on this device |
| **Backup** | Import/export (new Signal backup format via libsignal) | `warte export` | M3  | 🟡 `warte export` and `:export` write warte's own encrypted format (WARTEXP1, no attachments). There is no import, and Signal's backup format isn't supported |
| **Notifications** | Desktop notifications, privacy levels (name+message / name / none) | OSC 9/99/777 → native (macOS/libnotify) → bell; unread count in the terminal title | M1  | ✅ |
| **Settings** | Everything relevant from Desktop's settings | `:settings` Huh form plus `config.toml` | M2  | ✅ Read receipts, typing, link previews, sealed sender, number privacy, default timer, notifications, appearance |
| **Not planned** | Voice and video calls, payments (MobileCoin), donations/badges purchase, primary (standalone) registration | Calls and payments are shown as events; you handle them on your phone | —   | ❌ Out of scope |

## 7. Images and rich media
**Capability detection at startup** (query plus env sniffing, overridable in config):

1. **kitty graphics protocol** with **Unicode placeholders** (`U+10EEEE`). Kitty, Ghostty, and recent WezTerm support it. This is the key technique: the image is tied to text cells, so it scrolls, clips, and redraws correctly with Bubble Tea's cell-based renderer.
  
2. **iTerm2 inline images** (iTerm2, WezTerm).
  
3. **Sixel** (foot, xterm, Windows Terminal, …).
  
4. **Fallback:** Unicode half-blocks or braille with truecolor (chafa-style). It always works and still looks decent.
  

**Behavior**

- Thumbnails inline in the conversation (capped at N rows); `Enter` opens a full-pane viewer with zoom (`+`/`-`) and pan (`hjkl`).
  
- GIFs and stickers animate in kitty (via the protocol's animation frames); elsewhere they show the first frame with a ▶ badge.
  
- Video: poster frame inline; `Enter` hands off to `mpv` (configurable).
  
- Audio and voice notes: inline player with a braille waveform and progress bar; plays through miniaudio.
  
- Images are sent to the terminal **directly** (escape-sequence payload, `t=d`), never via temp files, so plaintext never touches disk.
  
- Images are lazy-loaded and LRU-cached in memory. Placements are deleted when messages scroll far out of view.
  
## 8. Look and feel
- **Themes:** ship `charm` (default, pink/purple), `catppuccin-*`, `tokyonight`, `gruvbox`, `rose-pine`, `nord`, `mono`. Plus `auto`, which follows the terminal's light/dark background via Lip Gloss adaptive colors.
  
- **Signal chat colors** map onto the theme palette for bubble accents.
  
- **Bubbles or plain lines:** `layout = "bubbles" | "irc"` (IRC style shows `12:41 <alice> hi`, for density lovers).
  
- **Nerd Font icons** when available, with ASCII/Unicode fallbacks.
  
- **Motion:** spring-animated scroll, popover fade-ins, typing dots. All of it can be turned off with `reduce_motion = true`.
  
- **Avatars:** small images in kitty and similar terminals, colored initials otherwise.
  
## 9. Configuration
`~/.config/warte/config.toml` (XDG; `~/Library/Application Support` fallback on macOS)

```toml
theme = "auto"
layout = "bubbles"
mouse = true
leader = " "
images = "auto"          # auto | kitty | iterm2 | sixel | blocks | off
notifications = "name_and_message"
editor = "$EDITOR"
open_with = { video = "mpv", default = "open" }

[keys.normal]
"<leader>m" = "chat.mute"
```
## 10. Security and privacy
- Uses **libsignal** for all cryptography. No custom crypto.
  
- **SQLCipher** database plus an encrypted attachment store. The DB key lives in the OS keychain (macOS Keychain, Secret Service/libsecret) with a passphrase fallback.
  
- Uses the alternate screen, so nothing ends up in scrollback. Optional **screen lock** (`:lock`, `<leader>l`, idle timeout from `[screen_lock]`) that replaces the TUI with a lock screen and asks for a passphrase. While locked, nothing of the account is drawn, kitty images are deleted from the terminal, the window title is just "warte" and notifications only say "New message". Without a passphrase check from the host, the first `:lock` sets a lock password (argon2id hash in the `lock` file next to `config.toml`, mode 0600). Wrong attempts back off exponentially.
  
- No plaintext temp files, with two exceptions that need a normal path for another program: "Open in external app" writes a 0600 temp file (`warte-open-<pid>-*`) that is deleted after two minutes or on quit, and warns once; composing in `$EDITOR` (`Ctrl-e`) writes the draft to a 0600 temp file (`warte-edit-<pid>-*.md`) that is deleted when the editor exits or warte quits. Files left behind by a warte that crashed are removed at the next start.
  
- Clipboard copies of view-once or disappearing content are blocked.
  
- Debug logs never contain message bodies or numbers unless `--log-sensitive` is set.
  
- Respects Signal's trademark policy: the project name must not contain "Signal".
  
## 11. Performance targets
- Cold start to a usable chat list: **< 300 ms** (local DB).
  
- Typing latency: **< 16 ms** per keystroke at 10k messages in the viewport history.
  
- Idle RAM: **< 80 MB**. Binary size: < 40 MB.
  
- Smooth on 300×80 terminals and usable on 80×24.
  
## 12. Milestones
|     | Goal |
| --- | --- |
| **M0 Spike** | Link via QR, receive and send text 1:1, minimal Bubble Tea UI. Validates the libsignal-ffi + signalmeow stack |
| **M1 Daily driver** | Chat list, groups, replies, reactions, receipts, notifications, keymap engine, themes, search |
| **M2 Media** | Image protocols, attachments, link previews, disappearing messages, group management, safety numbers, settings |
| **M3 Parity** | Stories, stickers, polls, pins, voice notes, backups, media gallery, chat folders |
| **M4 Stretch** | SSH access via Wish, plugin hooks |
## 13. Prior art to study
- **Signal-Desktop:** the behavioral reference.
  
- **libsignal:** protocol and crypto.
  
- **mautrix-signal / signalmeow:** a Go Signal service client.
  
- **signal-cli:** how another client handles linking and sync edge cases.
  
- **presage, gurk-rs:** Rust client library and TUI. Learn from their UX and from their gaps.
  
- **siggo, scli:** earlier TUIs.
  
- **lazygit, k9s, aerc, neomutt, Telescope.nvim:** keyboard UX patterns.
  
## 14. Decisions (resolved in review)
1. Foundation A vs B (§2): i{==s reusing Signal Desktop's actual TS code a hard==}{>>not at all<<}{id="c1" by="user" at="2026-10-16T10:20:58.587Z"} requirement, or is "official libsignal + Desktop as reference" enough?
  
2. {==Linked device only,==}{>>linked only<<}{id="c2" by="user" at="2026-10-16T10:21:07.104Z"} or also support standalone (primary) registration?
  
3. {==How important are calls==}{>>not important<<}{id="c3" by="user" at="2026-10-16T10:21:13.203Z"}? They're the biggest single cost (RingRTC + audio I/O).
  
4. {==Is the daemon/attach architecture==}{>>uh...no probably not<<}{id="c4" by="user" at="2026-10-16T10:21:23.847Z"} worth doing from day one?
  
## Names
Constraint: the name can't contain "Signal" (trademark). It should be short, typeable, and fit the Charm style.

| Name | Binary | Why |
| --- | --- | --- |
| **warte** ⭐ | `warte` | German *Warte*: medieval watchtowers on ridges, each in sight of the next, relaying alarms by fire and smoke. Also means "wait!". Short and typed on the home row |
| **smoke** | `smoke` | Smoke signals: the oldest signaling tech, running in the most old-school UI |
| **tincan** | `tincan` | Tin-can telephone: private, two-way, a bit whimsical |
| **semaphore** | `sema` | Flag signaling, and a nod to concurrency nerds |
| **flare** | `flare` | A bright signal in the dark (your terminal) |
| **heliograph** | `helio` | Signaling with mirrors and sunlight. Pretty word, pretty app |
| **morse** | `morse` | The original terminal-era messaging |
| **beacon** | `beacon` | Steady, trustworthy light |
| **whisper** | `whisper` | Nod to Whisper Systems (Signal's origin), but crowded by OpenAI Whisper |

**Decision: warte.** It's less likely to collide with existing projects than `smoke`, but we should still check GitHub, Homebrew and the AUR before publishing.

---
comments:
  c5:
    body: "Done. Option A (Go + libsignal + signalmeow) is now the chosen foundation in §2. Signal Desktop is only the reference for how features behave."
    at: "2026-10-16T10:30:00.000Z"
    re: c1
  c6:
    body: "Done. Removed primary registration from §3 and §6 and listed it under Not planned. warte always links to your phone, like Desktop does."
    at: "2026-10-16T10:30:00.000Z"
    re: c2
  c7:
    body: "Done. Calls are out of scope, which also removes the RingRTC and audio work. warte still shows incoming and missed calls and call history, and tells you to answer on your phone. The old M4 (calls) is gone and the stretch milestone is now M4. Voice note recording (M3) still needs microphone input, so let me know if you'd rather drop that too."
    at: "2026-10-16T10:30:00.000Z"
    re: c3
  c8:
    body: "Done. Removed daemon/attach mode and the JSON-RPC socket. warte runs as a single process and catches up on messages when it starts. The CLI is now just link, unlink and export."
    at: "2026-10-16T10:30:00.000Z"
    re: c4
