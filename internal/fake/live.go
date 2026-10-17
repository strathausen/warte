package fake

import (
	"strings"
	"time"

	"warte/internal/core"
)

// Live mode: a background loop that makes the demo world feel inhabited.
// Every LiveMinInterval–LiveMaxInterval (chosen by the seeded RNG) it makes a
// contact type and send a message, react to your latest message, or ring you.
// In 1:1 chats, contacts answer your messages with a canned witty reply.

// liveLines are canned messages per chat. "@{me}" mentions self.
var liveLines = map[core.ChatID][]string{
	ChatAlice: {
		"ok but have you seen the sky right now 🌅",
		"Hamburg update: found a flat with a **balcony**!!",
		"thursday still on? 🍷",
		"Elif just sent me a 40-minute video about tower masonry. I watched all of it. who am I",
	},
	ChatHike: {
		"new tower just dropped: 31 m, granite, 1880s. adding it to the list 📝",
		"who has my headlamp. I know one of you has my headlamp",
		"test batch of cardamom buns: success. __10/10, no notes__",
		"@{me} sauna booked?? 👀",
		"forecast for the 1st looks **clear** ☀️",
	},
	ChatBookClub: {
		"chapter 3 of the new one… I need to talk about ||the letter at the end||",
		"next meetup at mine? I'll bake",
		"the audiobook narrator is doing an accent and I'm obsessed",
	},
	ChatFamily: {
		"Who is coming for dinner on Sunday? 🍲",
		"Papa fixed the bike! (It only took 3 weeks)",
		"Minka caught a leaf. Very proud. 🐈",
	},
	ChatWork: {
		"deploy going out in 10, shout if you're mid-merge",
		"`main` is green again 🟢",
		"cake in the kitchen. someone's birthday or someone's guilt, unclear",
		"@{me} can you take a look at INC-79 when you're free?",
	},
	ChatDev: {
		"can you look at my PR when you get a sec? small one, promise",
		"the coffee machine on 3 is fixed. this is not a drill ☕",
	},
	ChatMom: {
		"Did you eat lunch? 🥗",
		"I saw a fox in the garden this morning! 🦊",
	},
	ChatJonas: {
		"you awake?",
		"first shift done. I can make a heart in the foam now ☕❤️",
	},
	ChatFelix: {
		"Doughlores says hi 🍞",
	},
}

// liveOrder fixes the iteration order over liveLines for determinism.
var liveOrder = []core.ChatID{ChatAlice, ChatHike, ChatBookClub, ChatFamily, ChatWork, ChatDev, ChatMom, ChatJonas, ChatFelix}

var liveCallers = []core.ChatID{ChatAlice, ChatMom, ChatDev, ChatJonas}

var liveReactions = []string{"❤️", "👍", "😂", "😮", "🔥", "🙏"}

// wittyReplies answer your 1:1 messages in live mode.
var wittyReplies = []string{
	"ha! noted. filing that under \"things I'll pretend I knew all along\" 📁",
	"bold of you to assume I'm not already on my way 🏃",
	"I've read that three times and agree with it more each time",
	"message received and relayed to the next watchtower 🔥",
	"10/10, no notes. well, one note: more emoji",
	"typing… deleting… typing… ok: **yes**.",
	"__warte__ — let me think. …ok yes",
	"I'm putting that on a mug ☕",
	"you had me at the first word honestly",
	"screenshotting this for the group chat (affectionately)",
}

func (s *Service) live() {
	for {
		s.mu.Lock()
		span := s.opts.LiveMaxInterval - s.opts.LiveMinInterval
		d := s.opts.LiveMinInterval
		if span > 0 {
			d += time.Duration(s.rng.Int64N(int64(span) + 1))
		}
		action := s.rng.IntN(10)
		s.mu.Unlock()
		if !s.sleep(d) {
			return
		}
		switch {
		case action < 6:
			s.liveMessage()
		case action < 8:
			if !s.liveReact() {
				s.liveMessage()
			}
		default:
			s.liveCall()
		}
	}
}

// liveAvailableLocked reports whether a chat can receive simulated activity.
func (s *Service) liveAvailableLocked(id core.ChatID) (*chatState, bool) {
	cs, ok := s.chats[id]
	if !ok || s.loggedOut || cs.left || cs.chat.MessageRequest {
		return nil, false
	}
	if s.exportChatLocked(cs).Blocked {
		return nil, false
	}
	return cs, true
}

// peersLocked lists the other participants of a chat.
func (s *Service) peersLocked(cs *chatState) []core.ACI {
	if aci := cs.chat.ID.ACI(); aci != "" {
		if aci == s.self.ACI {
			return nil
		}
		return []core.ACI{aci}
	}
	var out []core.ACI
	for _, m := range cs.chat.Members {
		if m.ACI != s.self.ACI {
			out = append(out, m.ACI)
		}
	}
	return out
}

// incomingLocked appends an incoming message written in demo markup.
func (s *Service) incomingLocked(cs *chatState, from core.ACI, markup string) *core.Message {
	markup = strings.ReplaceAll(markup, "@{me}", "@{"+string(s.self.ACI)+"}")
	m := s.newMessageLocked(cs, from)
	m.Body, m.Ranges, m.MentionsSelf = parseMarkup(markup, s.self.ACI)
	m.ReceivedAt = m.SentAt.Add(350 * time.Millisecond)
	cs.insert(m)
	return m
}

// deliverTyped shows a typing indicator for TypingDelay, then delivers the
// message. It reports false if the service closed meanwhile.
func (s *Service) deliverTyped(chat core.ChatID, from core.ACI, markup string) bool {
	s.mu.RLock()
	name := s.nameLocked(from)
	showTyping := s.settings.TypingIndicators
	s.mu.RUnlock()
	if showTyping {
		s.emit(core.TypingChanged{Chat: chat, Who: from, Name: name, Typing: true})
	}
	if !s.sleep(s.opts.TypingDelay) {
		return false
	}
	s.mu.Lock()
	var evs []core.Event
	if showTyping {
		evs = append(evs, core.TypingChanged{Chat: chat, Who: from, Name: name, Typing: false})
	}
	if cs, ok := s.liveAvailableLocked(chat); ok {
		m := s.incomingLocked(cs, from, markup)
		c := s.exportChatLocked(cs)
		evs = append(evs,
			core.MessageAdded{Message: s.exportMsgLocked(m), Notify: !c.Muted(s.now())},
			core.ChatUpdated{Chat: c})
	}
	s.emitAll(evs)
	s.mu.Unlock()
	return true
}

func (s *Service) liveMessage() {
	s.mu.Lock()
	var avail []*chatState
	for _, id := range liveOrder {
		if cs, ok := s.liveAvailableLocked(id); ok {
			avail = append(avail, cs)
		}
	}
	if len(avail) == 0 {
		s.mu.Unlock()
		return
	}
	cs := avail[s.rng.IntN(len(avail))]
	peers := s.peersLocked(cs)
	if len(peers) == 0 {
		s.mu.Unlock()
		return
	}
	from := peers[s.rng.IntN(len(peers))]
	lines := liveLines[cs.chat.ID]
	line := lines[s.rng.IntN(len(lines))]
	chat := cs.chat.ID
	s.mu.Unlock()
	s.deliverTyped(chat, from, line)
}

// liveReact makes someone react to your most recent message.
func (s *Service) liveReact() bool {
	s.mu.Lock()
	var target *core.Message
	var tcs *chatState
	now := s.now()
	for _, cs := range s.chats {
		if _, ok := s.liveAvailableLocked(cs.chat.ID); !ok || len(s.peersLocked(cs)) == 0 {
			continue
		}
		for i := len(cs.msgs) - 1; i >= 0; i-- {
			m := cs.msgs[i]
			if !m.Outgoing || m.Kind != core.KindNormal || m.RemoteDeleted || !visible(m, now) {
				continue
			}
			if target == nil || m.ID.Timestamp > target.ID.Timestamp {
				target, tcs = m, cs
			}
			break
		}
	}
	if target == nil {
		s.mu.Unlock()
		return false
	}
	peers := s.peersLocked(tcs)
	from := peers[s.rng.IntN(len(peers))]
	emoji := liveReactions[s.rng.IntN(len(liveReactions))]
	var evs []core.Event
	if s.setReactionLocked(target, from, emoji, false) {
		evs = append(evs, core.MessageUpdated{Message: s.exportMsgLocked(target)})
	}
	s.emitAll(evs)
	s.mu.Unlock()
	return true
}

// liveCall rings you, then records a missed call.
func (s *Service) liveCall() {
	s.mu.Lock()
	var avail []*chatState
	for _, id := range liveCallers {
		if cs, ok := s.liveAvailableLocked(id); ok {
			avail = append(avail, cs)
		}
	}
	if len(avail) == 0 {
		s.mu.Unlock()
		return
	}
	cs := avail[s.rng.IntN(len(avail))]
	from := cs.chat.ID.ACI()
	video := s.rng.IntN(3) == 0
	name := s.nameLocked(from)
	chat := cs.chat.ID
	s.mu.Unlock()

	s.emit(core.IncomingCall{Chat: chat, From: from, Name: name, Video: video, Ringing: true})
	if !s.sleep(s.opts.CallRing) {
		return
	}
	s.mu.Lock()
	var evs []core.Event
	evs = append(evs, core.IncomingCall{Chat: chat, From: from, Name: name, Video: video, Ringing: false})
	if cs, ok := s.liveAvailableLocked(chat); ok {
		m := s.newMessageLocked(cs, from)
		m.Kind = core.KindCall
		m.ExpiresIn = 0
		m.Call = &core.CallInfo{Video: video, Incoming: true, Missed: true, StartedAt: m.SentAt}
		cs.insert(m)
		c := s.exportChatLocked(cs)
		evs = append(evs,
			core.MessageAdded{Message: s.exportMsgLocked(m), Notify: !c.Muted(s.now())},
			core.ChatUpdated{Chat: c})
	}
	s.emitAll(evs)
	s.mu.Unlock()
}

// autoReply answers your 1:1 message after a short pause and typing.
func (s *Service) autoReply(chat core.ChatID) {
	if !s.sleep(s.opts.Latency) {
		return
	}
	s.mu.Lock()
	cs, ok := s.liveAvailableLocked(chat)
	if !ok || chat == core.DirectChatID(s.self.ACI) {
		s.mu.Unlock()
		return
	}
	from := cs.chat.ID.ACI()
	reply := wittyReplies[(s.replyIndex+s.rng.IntN(3))%len(wittyReplies)]
	s.replyIndex++
	s.mu.Unlock()
	s.deliverTyped(chat, from, reply)
}
