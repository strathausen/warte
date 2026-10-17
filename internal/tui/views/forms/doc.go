// Package forms holds warte's forms and dialogs: huh (charm.land/huh/v2)
// forms embedded as ui.Overlays and themed from the live theme.Styles
// (HuhTheme), list-style settings pages, the safety number view with a QR
// code, and the full-screen device-link model used before login.
//
// # Constructors
//
//	NewNewChat(ctx, initial)          new chat: phone / username / name → ResolveRecipient → ui.OpenChatMsg
//	NewNewGroup(ctx)                  new group wizard: details (huh) → members (fuzzy multi-select) → review → CreateGroup
//	NewGroupSettings(ctx, chatID)     group settings page → UpdateGroup / UpdateChat / LeaveGroup
//	NewContactSettings(ctx, chatID)   direct chat settings page → SetNickname / UpdateChat / Block / AcceptMessageRequest / ReportSpam
//	NewChatSettings(ctx, chatID)      either of the two above, by chat kind
//	NewSafetyNumber(ctx, aci)         60 digits in 12 groups of 5 + QR code; toggles SetVerified
//	NewProfileEditor(ctx)             given/family name, about, about emoji, avatar → UpdateProfile
//	NewSettings(ctx)                  core.Settings + local config, applied live (SettingsChangedMsg)
//	NewMemberPicker(ctx, title, exclude, onDone)  fuzzy contact multi-select
//	NewConfirm(ctx, title, text, yes, danger, onYes)  y/n dialog
//	NewQRView(ctx, title, data, caption)          any string as a QR code
//	LinkModel(linker, deviceName)     full-screen tea.Model for device linking (*Link)
//
// Helpers: HuhTheme(styles) builds a huh theme from the palette; NewQR /
// QR.Render / QR.FitQuiet render QR codes with half blocks ('▀', '▄', '█',
// ' ': one module per column, two per row, quiet zone included, explicit
// high-contrast colors; with the colorless mono theme the bitmap is
// inverted on dark terminals); MuteOptions / MuteUntil.
//
// # Keys
//
// Dialogs built on huh capture keys (ui.KeyCapturer) while open: tab /
// shift+tab move between fields, enter confirms a field (and submits on the
// last one), j/k or arrows move in selects, h/l or y/n toggle confirms,
// "/" filters long selects, esc and ctrl+c cancel (esc first clears an
// active select filter). Keymap context: ContextForm ("forms.form").
//
// The settings pages do not capture keys; they run in the keymap contexts
// ContextGroupSettings ("forms.group") and ContextChatSettings
// ("forms.chat"), so overlay and Normal-mode navigation apply: j/k, gg/G,
// ctrl+d/u, enter (edit / toggle / choose / open actions), h/l (cycle mute,
// flip toggles), q/esc close. Row shortcuts arrive as unbound raw keys:
//
//	group member      p promote/demote (confirm), x remove (confirm), enter action menu
//	join request      a approve, x deny (confirm), enter action menu
//
// Action menus (ContextActions, "forms.actions") take j/k + enter or their
// shortcut letters. The safety number view (ContextSafety, "forms.safety")
// toggles verification with v or enter. Confirm dialogs use
// keymap.OverlayConfirm (y / enter yes, n / esc / q no). The member picker
// (ContextMembers) and the new group wizard (ContextNewGroup) capture keys:
// type to filter, tab / shift+tab toggle, ↑/↓ or ctrl+n/p move, ctrl+u /
// ctrl+w edit the filter, enter continues (with nothing toggled it takes
// the highlighted match), esc clears the filter, then goes back / closes.
//
// The link screen: q / esc / ctrl+c cancel, r / enter retry after an error,
// c copies the link (OSC 52), enter continues after success (it also quits
// by itself after ContinueAfter). Result() returns the account,
// ErrLinkCanceled or the error.
//
// # Messages
//
// Emitted for the App: ui.OpenChatMsg (new chat / new group / member
// "Message"), ui.PushOverlayMsg (sub-dialogs), ui.CloseOverlayMsg,
// ui.ToastMsg (results and errors), SettingsChangedMsg{Config, Settings}
// (every settings change; account settings were already saved with
// Service.UpdateSettings) and, while EmitCommands is true (default),
// ui.RunCommandMsg "theme <name>" (debounced), "set layout=…",
// "set mouse|nomouse", "set reducemotion|noreducemotion" so the existing
// ex commands apply changes live until the App handles SettingsChangedMsg.
// Views refresh from ui.EventMsg (ChatUpdated, ContactUpdated, ChatRemoved,
// IdentityChanged). Everything else is internal (results of the views' own
// commands, delivered back through the App's "forward to top overlay"
// path).
//
// # Integration
//
// The package registers itself from init (Register), so a blank import in
// cmd/warte is enough:
//
//	actions:  chat.new (<leader>n) → NewNewChat, group.new (<leader>g) → NewNewGroup,
//	          app.settings → NewSettings
//	commands: :group [create|settings|leave], :settings (:prefs), :profile,
//	          :verify (:safety), :chatsettings (:chatprefs)
//
// Suggested further hooks for the integrator (not registered here, to avoid
// clashing with built-ins or sibling views): chat.info (gi) or a sidebar
// entry → NewChatSettings(ctx, ctx.Chat()); handle SettingsChangedMsg in
// the App (apply theme/layout/mouse/images/notifications/reduce motion,
// persist the config) and then set forms.EmitCommands = false; run
// LinkModel before tui.New when the store has no account (warte link).
package forms
