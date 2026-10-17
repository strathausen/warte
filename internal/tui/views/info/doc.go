// Package info holds warte's chat info sidebar and the informational
// overlays: message details, call log, pinned messages and the
// logged-out / connection-problem screen.
//
// # Constructors
//
//	NewSidebar(ctx ui.Context) *Sidebar                       pane component (not an overlay)
//	NewMessageDetails(ctx, m core.Message) *MessageDetails    overlay ("K")
//	NewMessageDetailsForSelected(ctx) *MessageDetails         overlay for ctx.Selected(), nil if none
//	NewCallLog(ctx) *CallLog                                  overlay ("<leader>c", ":calls")
//	NewPinnedMessages(ctx, chat, loaded []core.Message) *PinnedMessages   overlay (":pinned")
//	NewConnection(ctx, state core.ConnState, err error) *Connection       full-screen overlay
//	NewChoice(ctx, title, []Choice, selected) ui.Overlay      small picker emitting ex commands
//	NewMutePicker(ctx) / NewTimerPicker(ctx, current)         pickers used by the sidebar
//	PinnedFrom(msgs) []core.Message                           pinned messages, newest first
//	RenderPinnedStrip(ctx, chat, pinned, current, width) string   one-line header strip
//
// # Sidebar
//
// The Sidebar renders only its content; the App draws the pane border.
// Use it like this:
//
//	sb := info.NewSidebar(ctx)
//	cmd := sb.SetChat(chatID)              // on chat switch; loads asynchronously
//	sb.Focus() / sb.Blur()                 // when the pane gains / loses focus
//	sb, cmd = sb.Update(msg)               // forward: its own load results (any msg
//	                                       // the returned cmds produce), ui.EventMsg,
//	                                       // and ui.ActionMsg while focused
//	sb.View(width, height)                 // exactly height lines of width cells
//
// Feed keys in keymap context sb.KeyContext() (keymap.Sidebar) and pass
// the fired binding as ui.ActionMsg. Handled actions: cursor.down/up (j/k),
// cursor.top/bottom (gg/G), page.half_down/half_up, page.down/up,
// cursor.left/right (h/l: thumbnail in the media strip), item.select and
// chat.open (enter). Unbound j/k/h/l/G/enter key presses are accepted as a
// fallback.
//
// Content: avatar (image via ctx.Images when inline images are available,
// otherwise a colored initials tile), name with verified badge, about /
// group description, username, phone (or "hidden"), safety status, invite
// link state, mute and disappearing timer, a media strip of the last
// MaxMedia photos, an action list, then groups in common (direct chats) or
// members with admin badges (groups), join requests and pending invites.
//
// Enter on an item emits:
//
//	media thumbnail      ui.OpenChatMsg{Chat, Focus: &msgID}
//	group in common      ui.OpenChatMsg{Chat: group}
//	member               ui.OpenChatMsg{Chat: direct chat} (not for yourself)
//	Search in chat       ui.RunCommandMsg{SearchCommand}            "search"
//	Mute                 ui.PushOverlayMsg(NewMutePicker) → "mute 8h" / "mute"
//	Unmute               ui.RunCommandMsg{UnmuteCommand}            "unmute"
//	Disappearing         ui.PushOverlayMsg(NewTimerPicker) → "disappear 1w"
//	Safety number        ui.RunCommandMsg{SafetyNumberCommand}      "safety"
//	Block / Unblock      ui.RunCommandMsg{BlockCommand/UnblockCommand}
//	Group settings       ui.RunCommandMsg{GroupSettingsCommand}     "group settings"
//
// The command lines are package variables so the integrator can point them
// at whatever a sibling package registers.
//
// # Overlays and their keys
//
// Every overlay accepts ui.ActionMsg (keymap context
// keymap.Overlay(KeyContext())) and falls back to raw j/k/G/enter presses.
//
//	MessageDetails  KeyContextDetails "info.details"
//	                j/k, gg/G, ctrl+d/u scroll · enter jump to the message (OpenChatMsg
//	                with Focus) · K (message.info) or q/esc close.
//	                Refreshes on core.MessageUpdated / ChatUpdated, closes on
//	                MessageRemoved. Shows sender, kind, body preview, sent /
//	                received / server timestamps, edit time, delivery status (per
//	                recipient for groups, grouped Viewed/Read/Delivered/Sent/Failed;
//	                core has no per-recipient times), disappearing timer with a
//	                progress bar, attachment metadata (type, size, dimensions,
//	                flags, download state, caption), reactions and every edit
//	                revision with a word diff against the previous one.
//	CallLog         KeyContextCalls "info.calls"
//	                j/k, gg/G, ctrl+d/u · tab / m / h / l toggle All ↔ Missed ·
//	                enter opens the chat (OpenChatMsg) and closes. Reloads on
//	                core.IncomingCall and call messages.
//	PinnedMessages  KeyContextPinned "info.pinned"
//	                j/k · enter jumps to the message (OpenChatMsg with Focus) ·
//	                p / x unpin (svc.PinMessage, then a toast). Tracks
//	                MessageUpdated / MessageAdded / MessageRemoved.
//	Connection      KeyContextConnection "info.connection"
//	                logged out: enter / r emit RelinkMsg, q / esc quit warte
//	                (HandleBack returns tea.Quit). Disconnected / connecting:
//	                animated spinner (static with reduce_motion), esc dismisses,
//	                closes itself on core.ConnChanged{ConnConnected}; switches to
//	                the logged-out text on core.LoggedOut.
//	Choice pickers  KeyContextChoice "info.choice"; capture keys: j/k, 1-9
//	                pick directly, enter, q/esc cancel. Emit ui.RunCommandMsg.
//
// # Messages emitted
//
// ui.OpenChatMsg, ui.RunCommandMsg, ui.PushOverlayMsg, ui.ToastMsg,
// tea.Quit (logged-out screen) and RelinkMsg (defined here: the ui package
// has no "start linking" message; the integrator should start the
// core.Linker flow when it sees it).
//
// # Registered hooks (init)
//
//	keymap.ActCalls ("<leader>c")  → push NewCallLog
//	:calls (:calllog)              → push NewCallLog
//	:pinned (:pins)                → push NewPinnedMessages for the open chat
//	:details (:msginfo)            → push NewMessageDetailsForSelected
//	:connection                    → push NewConnection for svc.ConnState()
//
// # Suggested integration (not done here; internal/tui is not ours)
//
//   - K (keymap.ActMessageInfo) is handled by the App's built-in infoOverlay;
//     replace it with ui.Push(info.NewMessageDetails(ctx, *selected)).
//   - Replace App.renderSidebar with a Sidebar: SetChat on chat switch,
//     Focus/Blur with FocusSidebar, forward ui.EventMsg and the sidebar's
//     command results, and feed sidebar-context actions as ui.ActionMsg.
//   - Show RenderPinnedStrip above the conversation when PinnedFrom(loaded)
//     is non-empty; bind e.g. "gp" to cycle it and "<leader>p" / :pinned to
//     open NewPinnedMessages(ctx, chat, loaded).
//   - Push NewConnection on core.LoggedOut (and optionally after a long
//     ConnDisconnected); handle RelinkMsg by starting the linking flow.
//   - Register commands matching SafetyNumberCommand and
//     GroupSettingsCommand (or reassign those variables).
package info
