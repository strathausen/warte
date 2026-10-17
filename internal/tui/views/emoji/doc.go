// Package emoji provides warte's emoji picker, reaction detail and sticker
// picker overlays (ui.Overlay implementations).
//
// # Constructors
//
//	NewPicker(ctx, Options{Purpose, Target, OnPick, Recents, Query}) *Picker
//	NewReactionPicker(ctx, target core.MessageID) *Picker  // Purpose "react"
//	NewInsertPicker(ctx) *Picker                           // Purpose "insert"
//	NewReactions(ctx, id core.MessageID) *Reactions        // loads svc.Message
//	NewReactionsFor(ctx, m core.Message) *Reactions        // pre-filled, refreshes on Init
//	NewStickers(ctx, StickerOptions{Chat, Recents, OnSend}) *Stickers
//
// Convenience commands (tea.Cmd that push the overlay or toast a warning):
// OpenReactionPicker, OpenInsertPicker, OpenReactions, OpenStickers.
//
// # Emoji picker (key context "emoji", keymap.Overlay(KeyContextPicker))
//
// Category tabs (recent/frequently used first, then format.Categories()
// with their icons), a grid of 4-cell cells whose column count follows the
// width, live search by name or shortcode, a detail line (name and
// :shortcode:) and a skin-tone indicator.
//
//	h j k l / arrows   move (counts work: 3l)        ActCursor*
//	gg G ctrl+d/u/f/b  top, bottom, half page, page  ActCursorTop/Bottom, ActHalfPage*, ActPage*
//	tab shift+tab      next / previous category      ActFocusNext/Prev (also H/L, gt/gT)
//	/                  search (captures keys)        ActSearchForward (raw "/" too)
//	t T                cycle skin tone forward/back  raw keys
//	enter              pick                          ActItemSelect
//	q esc              clear search, then close      ActBack (BackHandler)
//	mouse              click tab / emoji, wheel scrolls
//
// While searching (CaptureKeys true): type to filter, backspace, ctrl+u,
// ctrl+w; up/down/left/right and ctrl+n/p/j/k move; enter picks; tab leaves
// the field keeping the results; esc leaves the field (and the search when
// the query is empty); ctrl+c closes.
//
// On enter the picker records the emoji in Recents, closes, and runs
// Options.OnPick(PickedMsg{Emoji, Purpose, Target}); the default (Handle)
// calls Service.React for "react" (removing it when you already reacted with
// that emoji; error → ui.ToastMsg) and returns ui.ComposeInsertMsg for
// "insert". To receive PickedMsg yourself, pass
// OnPick: func(m PickedMsg) tea.Cmd { return func() tea.Msg { return m } }.
//
// # Reaction details (key context "reactions")
//
// Tabs "All N" and one per emoji ("👍 3", most used first); rows show the
// emoji, avatar, sender name (your own row says "You") and when.
//
//	j k / gg G / ctrl+d/u  move                 ActCursor*, ActHalfPage*
//	tab shift+tab h l      switch emoji tab     ActFocusNext/Prev, ActCursorLeft/Right (H/L, gt/gT)
//	enter                  remove, on your row  ActItemSelect
//	x d delete             remove your reaction raw keys
//	+ a R                  add a reaction       raw keys → ui.PushOverlayMsg(NewReactionPicker)
//	mouse                  click tab / row, wheel
//
// Refreshes on ui.EventMsg{core.MessageUpdated} for the message and shows
// "deleted" on core.MessageRemoved. Removal is optimistic; errors toast.
//
// # Sticker picker (key context "stickers")
//
// Tabs: "Recent" (only when stickers were sent this session) then the packs
// from Service.StickerPacks, installed first (uninstalled ones are marked
// "+"). Each sticker is drawn through ui.Context.Images (kitty placeholders
// or half-blocks) in a 6x3 cell area with a margin and its emoji below; when
// images are disabled an emoji tile is shown instead.
//
//	h j k l / arrows   move                    ActCursor*
//	tab shift+tab      next / previous pack    ActFocusNext/Prev (H/L, gt/gT)
//	enter              send to the open chat   ActItemSelect → Service.Send(core.Outgoing{Sticker})
//	i                  install / remove pack   raw key → Service.InstallStickerPack, toast, reload
//	mouse              click tab / sticker, wheel
//
// The target chat is StickerOptions.Chat or ctx.Chat(); with none open the
// picker stays open and toasts a warning. Send errors become error toasts.
//
// # Messages emitted
//
// ui.ComposeInsertMsg (insert picks), ui.ToastMsg (errors, install results,
// warnings), ui.PushOverlayMsg (reaction picker from the details view) and
// PickedMsg when the caller's OnPick asks for it. Reactions and stickers are
// sent by calling the Service directly from commands.
//
// # Recents
//
// Recents (Session by default) keeps recently/frequently used emojis, recent
// stickers and the skin tone in memory for the session. Hooks for
// persistence: SetEmojis, SetStickers, SetSkinTone to restore, OnChange to be
// told when to save; Emojis, Frequent, Stickers, SkinTone to read.
//
// # Registered hooks (init)
//
//   - ui.RegisterAction(ui.ActReactionPicker, OpenReactionPicker): the "…"
//     entry of the quick reaction bar opens the full picker for
//     ctx.Selected().
//   - :emoji [query]  — insert picker (query pre-fills the search)
//   - :stickers       — sticker picker for the open chat (alias :sticker)
//   - :reactions      — reaction details for the selected message
//
// # Suggestions for the integrator
//
//   - Blank-import this package from cmd/warte.
//   - Keymap defaults (none exist yet for these contexts; the views also
//     accept the raw keys listed above): in Overlay("emoji") bind "t" to a
//     tone action if one is added; in Conversation bind e.g. "gR" or
//     "<leader>r" to OpenReactions and Insert-mode "<C-s>" to OpenStickers,
//     "<C-.>" (or "<A-e>") to OpenInsertPicker.
//   - Enter/click on a message's reaction pills could call OpenReactions.
//   - The App's ui.ReactMsg path also records the last emoji for ".";
//     this package calls Service.React directly as specified, so "." does not
//     repeat picker reactions unless OnPick is set to emit ui.ReactMsg.
package emoji
