package fake

import (
	"time"

	"warte/internal/core"
)

// The demo world: Robin Weber (self) and friends, planning a hike to the
// Hohe Warte watchtower, arguing about a novel, wrangling family logistics and
// keeping a platform team's pager quiet.

const (
	moodHappy = iota
	moodSurprised
	moodSleepy
	moodLove
	moodWink
	moodSad
	moodCool
	moodExcited
)

var (
	critterPackID  = hexFor("pack/trail-critters", 32)
	critterPackKey = hexFor("key/trail-critters", 64)
	towerPackID    = hexFor("pack/watchtower-moods", 32)
	towerPackKey   = hexFor("key/watchtower-moods", 64)
)

var critterSpecs = []stickerSpec{
	{"🦊", 0xf4a261, moodHappy},
	{"🦉", 0x8d6e63, moodSurprised},
	{"🐻", 0x6d4c41, moodSleepy},
	{"🐰", 0xd9d9d9, moodLove},
	{"🦝", 0x9e9e9e, moodWink},
	{"🐸", 0x7cb342, moodSad},
	{"🐧", 0x37474f, moodCool},
	{"🐿️", 0xd2691e, moodExcited},
}

var towerSpecs = []stickerSpec{
	{"🔥", 0xff7043, moodExcited},
	{"👀", 0x90a4ae, moodSurprised},
	{"😴", 0x5c6bc0, moodSleepy},
	{"🫡", 0x26a69a, moodCool},
}

func (s *Service) seedStickers(b *builder) {
	mk := func(id, key, title, author string, specs []stickerSpec, installed bool) *core.StickerPack {
		p := &core.StickerPack{ID: id, Key: key, Title: title, Author: author, Installed: installed}
		for i, sp := range specs {
			data := stickerPNG(sp, 128)
			k := stickerKey{pack: id, id: uint32(i)}
			s.stickers[k] = data
			st := core.Sticker{
				PackID: id, PackKey: key, StickerID: uint32(i), Emoji: sp.emoji,
				Attachment: core.Attachment{
					ID:          "sticker-" + id[:8] + "-" + string(rune('0'+i)),
					ContentType: "image/png",
					Size:        int64(len(data)),
					Width:       128,
					Height:      128,
					Borderless:  true,
					Downloaded:  true,
					Progress:    1,
				},
			}
			p.Stickers = append(p.Stickers, st)
		}
		cover := p.Stickers[0]
		p.Cover = &cover
		return p
	}
	s.packs = []*core.StickerPack{
		mk(critterPackID, critterPackKey, "Trail Critters", "Elif Yılmaz", critterSpecs, true),
		mk(towerPackID, towerPackKey, "Watchtower Moods", "warte", towerSpecs, false),
	}
}

// sticker scripts a sticker message from the Trail Critters pack.
func (c *convo) sticker(who core.ACI, idx int) *msg {
	c.advance()
	st := c.b.s.packs[0].Stickers[idx]
	return c.add(&core.Message{
		ID:       core.MessageID{Chat: c.cs.chat.ID, Author: who},
		Kind:     core.KindNormal,
		Outgoing: who == c.b.s.self.ACI,
		SentAt:   c.cur,
		Sticker:  &st,
	})
}

func (s *Service) seedChats(b *builder) {
	s.seedHike(b)
	s.seedAlice(b)
	s.seedBookClub(b)
	s.seedFamily(b)
	s.seedWork(b)
	s.seedTrailAlerts(b)
	s.seedDev(b)
	s.seedMom(b)
	s.seedFelix(b)
	s.seedNoor(b)
	s.seedJonas(b)
	s.seedMarco(b)
	s.seedInes(b)
	s.seedMax(b)
	s.seedNoteToSelf(b)
}

func (s *Service) seedHike(b *builder) {
	const me = SelfACI
	A, B, E, F := AliceACI, BobACI, ElifACI, FelixACI
	cs := s.group(ChatHike, "Warte Wanderers 🏔️",
		"Ridges, watchtowers & questionable snacks. Next up: Hohe Warte loop, Saturday.",
		[]core.ACI{A, B, E, F}, me, A)
	cs.chat.Pinned, cs.chat.PinOrder = true, 1
	cs.chat.InviteLink = "https://signal.group/#CjQKIBx3d2FydGUtd2FuZGVyZXJzLWRlbW8tbGluaxIQ"
	cs.chat.AddFromLinkMode = core.AccessMember
	cs.chat.RequestingJoin = []core.Member{{ACI: InesACI}}
	c := b.convo(cs)

	c.day(19, 20, 2)
	c.sys("Alice created the group.")
	c.sys("Alice added you, Bob and Elif.")
	c.say(A, "ok you lot. we said \"we should hike more\" four times this summer. this is the group where that actually happens 🥾")
	c.say(E, "finally!! I have a list of 23 watchtowers within two hours of here and nobody to drag up them")
	c.say(B, "23?? Elif")
	c.say(E, "24 if you count the one that's technically a water tower. I don't.")
	c.me("I'm in. Two conditions: there's a view, and someone else navigates")
	c.say(E, "the view is the whole point. navigation is my love language")
	c.sys("Alice changed the group name to “Warte Wanderers 🏔️”.")
	c.say(A, "named it after the towers. *Warte* = lookout. also means \"wait!\" which is what Bob will be shouting on every incline")
	c.say(B, "slander. accurate slander, but slander")

	c.day(17, 19, 15)
	c.say(E, "Proposal for the first one: **Hohe Warte** loop.\n\n• 14.2 km, ~620 m up\n• stone tower from 1342, 28 m, 119 steps\n• on a clear day you can see three other towers from the top — they used to relay fire signals ridge to ridge\n• there's a hut at the halfway point that does Kaiserschmarrn")
	c.say(B, "you had me at Kaiserschmarrn")
	m1 := c.say(A, "fire signals ridge to ridge is the coolest thing I've heard all week")
	c.me("medieval group chat").reply(m1).react(A, "😂", E, "😂", B, "💀")
	c.say(E, "literally. one fire = all good, two fires = raiders, no fire = Kevin fell asleep again")
	c.say(E, "https://www.openstreetmap.org/relation/4206942").link(core.LinkPreview{
		URL:         "https://www.openstreetmap.org/relation/4206942",
		Title:       "Hohe Warte Rundweg · OpenStreetMap",
		Description: "Hiking route · 14.2 km · Rundweg über den Wartberg mit Aussichtsturm",
		Image:       ptr(b.art(240, 160, patternWaves, 0x9fd8a0, 0x2e6b3f, "route.png")),
	})
	c.say(F, "who's Kevin")
	c.say(E, "there's always a Kevin")

	c.day(15, 18, 40)
	c.sys("Felix joined the group via the group link.")
	c.say(F, "hello hello, Elif said there'd be bread-related responsibilities")
	c.say(E, "there are now")
	c.say(A, "welcome Felix 🍞")
	c.sticker(F, 0)

	c.day(13, 9, 5)
	p := c.say(E, "")
	p.m.Poll = &core.Poll{
		Question:      "Which Saturday works?",
		AllowMultiple: true,
		Options: []core.PollOption{
			{Text: "Oct 4", Voters: []core.ACI{B}},
			{Text: "Oct 11", Voters: []core.ACI{E, A, me, F}},
			{Text: "Oct 18", Voters: []core.ACI{E, B}},
		},
	}
	c.say(B, "11th I'd have to leave by 5 for a birthday thing, but that's fine if we start early")
	c.say(E, "early start it is. sunrise from the tower?? 👀")
	c.say(A, "how early is sunrise early")
	c.say(E, "7:28. so we'd leave at… 5:15")
	c.say(B, "I take back everything nice I ever said about you")
	c.me("I'll bring the big thermos. it's the size of a small child")
	c.say(A, "the legendary thermos").react(E, "☕", B, "🙏")

	c.day(12, 21, 10)
	gear := c.me("**Packing list** (please read, Bob 🫶)\n\n__must__\n• headlamp — it's dark until 7\n• layers, it's ~2°C at the top before sunrise\n• water, 1L+\n• proper shoes, the last bit is roots and wet stone\n\n__nice to have__\n• gloves\n• sit pad\n• snacks to share\n\n~~Kaiserschmarrn money~~ the hut is cash only, bring €15").
		edited("**Packing list**\n\n• headlamp\n• layers\n• water\n• shoes",
			"**Packing list** (please read, Bob)\n\n__must__\n• headlamp\n• layers, it's cold at the top\n• water, 1L+\n• proper shoes").pin()
	c.say(B, "why am I singled out").reply(gear)
	c.say(A, "you know why").react(E, "😂", me, "😂")
	c.say(B, "ONE TIME I brought flip-flops")
	c.say(F, "to a glacier")
	c.say(B, "it was a small glacier")

	c.day(11, 12, 30)
	c.say(E, "Here's the plan for the route. Elevation profile looks scarier than it is, the steep bit is only ~800 m").
		attach(b.art(480, 200, patternWaves, 0xe9f5db, 0x386641, "profile.png"))
	c.say(A, "that spike at km 6 is not \"not scary\" Elif")
	c.say(E, "it's a gentle spike")
	c.say(F, "gentle spike is my band name")
	c.say(B, "I'll bring snacks as an apology in advance for how slow I'll be")
	c.say(A, "snack update when you know, so we don't end up with 4 bags of trail mix again")
	c.say(B, "trail mix is a personality. but fine. I'm thinking: pretzels, the good chocolate, mandarins")
	c.say(F, "I'll bake. rye & caraway loaf, plus some Franzbrötchen if the dough cooperates")
	c.say(E, "Felix we are so lucky to have you")
	c.say(F, "you're lucky my starter is in a good mood this week")

	c.day(9, 22, 45)
	c.say(E, "Forecast for Saturday: clear, 1°C at 6am, wind 15 km/h on the ridge. **Perfect** sunrise conditions ☀️")
	c.say(A, "1°C 🥶")
	c.me("thermos is non-negotiable then")
	c.say(B, "can we discuss the meeting point? parking at the Wartberg lot is ~€6 and fills up")
	c.say(E, "we'll be there at 5:45, it'll be empty. Just us and the owls")
	gif := c.say(A, "mood:").attach(b.store("gif", "image/gif", "signal-fire.gif", signalFireGIF()))
	gif.m.Attachments[0].GIF = true
	gif.react(E, "🔥", F, "🔥")

	c.day(8, 18, 2)
	c.say(B, "carpool: I can take 3 in the Golf. leaving from Hauptbahnhof 5:00 sharp")
	c.say(F, "5:00 sharp in Bob time or real time")
	c.say(B, "both are the same now. I'm a changed man")
	c.me("I'll take the train to Hbf and hop in")
	c.say(A, "same 🙋‍♀️")
	c.say(E, "I'll drive from mine with Felix then 👍")

	c.day(6, 5, 2)
	c.say(B, "I'm up. I'm awake. this is a war crime")
	c.say(A, "the stars though 🌌")
	c.say(E, "at the lot. it's -1, not 1. bring the gloves you didn't pack")
	c.say(F, "bread is still warm, it's basically a hand warmer")

	c.day(6, 7, 31)
	top := c.say(E, "").attach(b.photo(640, 360, sceneSunrise, "sunrise.png", "7:31 from the top. three towers visible 🔥🔥🔥")).
		react(A, "😍", B, "😍", me, "❤️", F, "🤩")
	c.say(A, "I think I'm crying a little? might be the wind").reply(top)
	c.me("worth the 5am. every second of it")
	c.say(B, "ok. ok I see it now. Elif you were right")
	c.say(E, "I'd like that in writing")
	c.say(B, "you'll have to take a screenshot, I'll never say it again")
	c.say(F, "").attach(b.photo(320, 480, sceneNoon, "tower.png", "119 steps, I counted"))
	c.say(A, "Felix in portrait mode 😂")

	c.day(6, 11, 50)
	c.say(B, "Kaiserschmarrn was life-changing. I want to marry the hut lady")
	c.say(E, "Her name is Waltraud and she said you can come back anytime as long as you don't sing again")
	c.say(A, "the singing was a choice")
	del := c.say(B, "")
	del.deleted()
	c.say(F, "Bob what did you delete")
	c.say(B, "nothing. a video. of nothing")
	c.say(A, "I have the same video from a different angle 😇")

	c.day(5, 20, 15)
	c.say(E, "photo dump from Saturday!! 📸")
	c.say(E, "").attach(
		b.photo(480, 320, sceneLake, "lake.png", ""),
		b.photo(400, 400, sceneNoon, "ridge.png", ""),
		b.photo(320, 240, sceneDusk, "dusk.png", ""),
	)
	c.say(A, "the lake one should be on a postcard")
	c.me("the dusk one is going on my wall. who's got the drive link for the full set?")
	c.say(E, "uploading the raws tonight, I'll share it here")

	c.day(2, 19, 30)
	c.say(A, "so. next one?? I'm thinking Rosenwarte, bit further but there's a lake with a sauna at the end")
	c.say(F, "a hike that ends in a sauna is the correct kind of hike")
	c.say(E, "Rosenwarte is 18 km and the tower is wooden, 34 m. bit wobbly in wind 😬")
	c.say(B, "\"bit wobbly\" is doing a lot of heavy lifting")

	c.day(0, 8, 12)
	c.say(E, "ok proposing Rosenwarte on the 1st (Nov). Leaving 7:00 this time because I'm merciful")
	c.say(A, "MERCY 🙏")
	c.say(B, "@{"+string(me)+"} you're bringing the thermos again right? it's a load-bearing part of this friendship")
	c.say(F, "I'll bake again. requests?")
	c.say(A, "the cardamom buns. please. I'll do anything")
	c.say(E, "@{"+string(me)+"} can you book the sauna slot? you're the only one organised enough to remember")
	c.say(B, "also Robin owes me a mandarin")
	c.unreadTail(7)
}

func (s *Service) seedAlice(b *builder) {
	const me = SelfACI
	A := AliceACI
	cs := s.direct(A)
	cs.chat.Pinned, cs.chat.PinOrder = true, 0
	c := b.convo(cs)

	c.day(21, 18, 3)
	c.say(A, "are you still alive or did the new job eat you")
	c.me("half eaten. the onboarding docs are 140 pages")
	c.say(A, "140!!! what's on page 1")
	c.me("a glossary. of the glossary")
	c.say(A, "I'm so sorry 😂 drinks thursday to recover?")
	c.me("yes please. the usual place?")
	c.say(A, "the usual place 🍷")

	c.day(20, 13, 40)
	c.call(A, core.CallInfo{Missed: true})
	c.say(A, "called by accident, pocket dial sorry!!")
	c.me("my phone thought you were being kidnapped")

	c.day(18, 22, 10)
	c.say(A, "have you watched the finale yet")
	c.me("no!! don't tell me anything")
	c.say(A, "I won't. but ||the lighthouse keeper was the narrator the whole time|| 🙊").
		edited("I won't. but the lighthouse keeper was the narrator the whole time")
	c.me("ALICE")
	c.say(A, "it's in a spoiler!! you have to click it!!")
	c.me("I have no self control and you know it")

	c.day(17, 8, 50)
	c.say(A, "look at this").attach(b.photo(480, 320, sceneSunrise, "commute.png", ""))
	c.me("where is that??")
	c.say(A, "the bridge on my way to work. I've walked past it a thousand times and today it just hit different")

	c.day(15, 19, 22)
	c.say(A, "")
	c.lastMsg().attach(withVoice(b.store("voice", "audio/ogg", "", voiceNoteOgg(14))))
	c.me("ok that laugh at the end got me 😂")
	c.say(A, "I couldn't hold it together. the cat. the CAT")

	c.day(14, 20, 5)
	c.call(me, core.CallInfo{Video: true, Duration: 23*time.Minute + 14*time.Second})
	c.say(A, "thanks for the call. I feel better about the interview")
	c.me("you'll be great. you know this stuff better than anyone")
	c.say(A, "🫶")

	c.day(12, 9, 15)
	c.say(A, "good luck wishes accepted until 10:00")
	c.me("**GOOD LUCK** 🍀🍀🍀 go get them")
	c.day(12, 11, 48)
	c.say(A, "they want a second round!!!").react(me, "🎉")
	c.me("OF COURSE THEY DO")

	c.day(10, 17, 30)
	c.say(A, "https://www.theguardian.com/travel/watchtowers-of-the-black-forest").link(core.LinkPreview{
		URL:         "https://www.theguardian.com/travel/watchtowers-of-the-black-forest",
		Title:       "Ridge to ridge: walking between the old watchtowers",
		Description: "Medieval lookout towers once relayed warnings across valleys by fire. Today they make for some of the best day hikes in the country.",
		Image:       ptr(b.photo(300, 160, sceneDusk, "towers.png", "")),
	})
	c.say(A, "Elif is going to lose her mind when she sees this")
	c.me("she already sent it to me. twice. and to the group")
	c.say(A, "of course she did")

	c.day(8, 21, 0)
	m := c.say(A, "can I ask you something kinda serious")
	c.me("always")
	c.say(A, "if I get this job it's in Hamburg. like, moving to Hamburg.\n\nand I'm excited but also I've lived here 9 years and everyone I love is here and I don't know if I'm being brave or stupid.\n\nno need to answer now, I just needed to say it to someone")
	c.me("hey. it can be both. brave things usually feel a bit stupid from the inside.\n\nand Hamburg is 1h40 on the ICE. we'll come visit so much you'll get sick of us").reply(m)
	c.say(A, "promise?")
	c.me("promise. Elif has probably already found a watchtower there")
	c.say(A, "😭❤️")

	c.day(7, 10, 20)
	c.say(A, "trail cam footage from the cabin, there's a fox!!").
		attach(withSize(b.store("vid", "video/mp4", "trail-cam.mp4", mp4Placeholder(48_000)), 640, 360))
	c.me("is it carrying a sandwich")
	c.say(A, "it is 100% carrying a sandwich")

	c.day(4, 18, 30)
	c.call(A, core.CallInfo{Duration: 41 * time.Minute})
	c.say(A, "sorry I talked your ear off")
	c.me("my ear is fine. my phone is at 3% though")

	c.day(1, 21, 15)
	c.say(A, "they offered!!!!!!!")
	c.me("AAAAAA 🎉🎉🎉")
	c.me("I knew it. I KNEW it")
	c.say(A, "I said I'd answer by Friday. I think I'm going to say yes")
	c.me("proud of you. genuinely")
	c.say(A, "drinks on me thursday. no arguments")

	c.day(0, 12, 40)
	c.me("look at this, the view from the office today").attach(b.photo(360, 270, sceneNoon, "office.png", ""))
	c.me("ok that's not the office. I'm working from the park").status(core.StatusDelivered)
	c.say(A, "haha yes, send the pic of your laptop on the bench, I need proof")
	c.unreadTail(1)
}

func (s *Service) seedBookClub(b *builder) {
	const me = SelfACI
	C, H, A, J := ClaraACI, HanaACI, AliceACI, JonasACI
	cs := s.group(ChatBookClub, "Book club 📚",
		"One book a month. Spoilers in ||spoiler tags|| please. Meetings: last Sunday, Clara's place.",
		[]core.ACI{C, H, A, J}, C)
	cs.chat.MutedUntil = b.now.Add(8 * time.Hour)
	c := b.convo(cs)

	c.day(24, 19, 0)
	c.say(C, "October book is **The Lantern Keepers** by Ilse Marrow. ~380 pages, so ~95 a week if you're the planning type")
	c.say(H, "I'm the planning type 📅")
	c.say(J, "I'm the night-before type")
	c.say(C, "we know, Jonas")
	c.me("got it from the library, there's a waiting list of 11 behind me so no pressure")
	c.say(A, "I'm doing the audiobook, the narrator has the most soothing voice").
		react(H, "🎧")

	c.day(20, 21, 30)
	c.say(H, "chapter 4 ending!!!! ||Maren knew about the letters all along||")
	c.say(C, "HANA the tags")
	c.say(H, "I used the tags!!")
	c.say(J, "I haven't started yet so I'm just going to mute this chat until sunday")
	c.say(A, "you say that every month")

	c.day(16, 8, 10)
	c.say(C, "Discussion questions for Sunday (I'll print them, but here for reference):\n\n1. Why does the book never show us the lanterns being lit?\n2. Is Maren a reliable narrator? Pages 140–170 especially.\n3. The tower is described as \"listening\" — what's it listening for?\n4. Did the ending earn its quiet?\n\nBring snacks, I'll do tea.")
	c.say(H, "question 3 has been living in my head rent free")
	c.me("I think the tower is listening for the next tower. that's the whole point of them")
	c.say(C, "oooh. hold that thought for Sunday").react(H, "💡", A, "👀")
	c.say(A, "the chain-of-towers thing is so Robin-coded. Elif has converted you")

	c.day(13, 14, 20)
	c.say(J, "update: I am on page 40")
	c.say(C, "Jonas it is the 13th")
	c.say(J, "page 40 of a GOOD book")
	c.sticker(me, 2)

	c.day(10, 20, 45)
	c.say(H, "").attach(b.art(300, 420, patternStripes, 0xf2e8cf, 0x6a994e, "cover.png"))
	c.lastMsg().m.Attachments[0].Caption = "the special edition cover is so pretty 🥹"
	c.say(C, "I want it. I want it so badly")
	c.say(A, "there's a signed one at the shop on Lindenstraße btw")

	c.day(6, 18, 30)
	c.say(C, "Sunday at mine, 4pm. Hana's bringing her famous matcha cake")
	c.say(H, "\"famous\" 🙈")
	c.say(A, "it IS famous. people talk about it")
	c.me("I'll bring the wine")
	c.say(J, "I'll bring my opinions on the first 200 pages")

	c.day(4, 21, 50)
	c.say(C, "Thank you all for tonight. That was one of our best discussions!\n\nFor the record: Jonas's theory that the lighthouse and the tower are the same building is **wrong** but also kind of beautiful.")
	c.say(J, "history will vindicate me")
	c.say(H, "it was such a nice evening ☺️").react(C, "❤️", A, "❤️", me, "❤️")
	poll := c.say(C, "")
	poll.m.Poll = &core.Poll{
		Question: "November book?",
		Options: []core.PollOption{
			{Text: "Small Hours of the Fen", Voters: []core.ACI{H, me}},
			{Text: "A Field Guide to Leaving", Voters: []core.ACI{A, C, J}},
			{Text: "The Cartographer's Daughter", Voters: nil},
		},
		Ended: true,
	}
	c.say(C, "Poll closed — **A Field Guide to Leaving** it is!")
	c.say(A, "seems fitting given everything 😅")
	c.say(H, "🫂")

	c.day(1, 10, 5)
	c.say(H, "started it this morning, first line is 🔥")
	c.say(C, "no quotes yet, some of us are still waiting for the library 😤")
	c.unreadTail(2)
}

func (s *Service) seedFamily(b *builder) {
	const me = SelfACI
	M, D, J := MomACI, DadACI, JonasACI
	cs := s.group(ChatFamily, "Weber Family 🏡", "", []core.ACI{M, D, J}, M, D)
	c := b.convo(cs)

	c.day(22, 9, 0)
	c.say(M, "Good morning my darlings ☀️ Reminder: Oma's 85th is on the 2nd. Lunch at 1, please don't be late. JONAS.")
	c.say(J, "why am I always singled out")
	c.say(D, "Track record.")
	c.me("I'll be there. Should I bring anything?")
	c.say(M, "Just yourselves! And maybe a salad. And the folding chairs if you still have them. And flowers for Oma, the yellow ones she likes")
	c.say(J, "\"just yourselves\" 😂")

	c.day(19, 19, 42)
	c.say(D, "Has anyone seen my reading glasses.")
	c.say(M, "On your head.")
	c.say(D, "Thank you.")
	c.say(J, "classic")

	c.day(16, 17, 0)
	c.say(M, "").attach(b.art(400, 300, patternCircles, 0xffe5ec, 0xfb6f92, "cake.png"))
	c.lastMsg().m.Attachments[0].Caption = "Test cake for Oma! Raspberry & almond 🎂"
	c.lastMsg().react(J, "🤤", me, "😍", D, "👍")
	c.say(J, "is there a second test cake. for testing")
	c.say(M, "There's half of it in the fridge, but it's for Papa")
	c.say(D, "It was for Papa.")

	c.day(14, 11, 5)
	c.sys("Your safety number with Papa has changed. Tap to verify.")
	c.say(D, "New phone. The man at the store said it is very fast.")
	c.say(J, "what did you get")
	c.say(D, "A black one.")
	c.me("perfect answer")

	c.day(12, 20, 30)
	c.call(M, core.CallInfo{Group: true, Video: true, Duration: 34 * time.Minute})
	c.say(M, "So nice to see all your faces! Jonas, please eat a vegetable")
	c.say(J, "fries are potatoes and potatoes are vegetables")
	c.say(D, "That is technically correct.")
	c.say(M, "Don't encourage him Thomas")

	c.day(9, 8, 15)
	c.say(M, "").attach(withVoice(b.store("voice", "audio/ogg", "", voiceNoteOgg(38))))
	c.say(J, "mama it's 8am why is there a 40 second voice message")
	c.say(M, "Listen to it!")
	c.me("summary for Jonas: Oma wants us to bring the photo albums from the attic, and the car needs to be moved Tuesday for the street cleaning")
	c.say(J, "🙏 the hero we need")

	c.day(5, 12, 0)
	c.say(D, "Folding chairs are in my car. 6 of them. Do we need more.")
	c.say(M, "We need 9!")
	c.me("I have 3, I'll bring them")
	c.say(D, "Good.")

	c.day(3, 15, 30)
	c.say(M, "Oma loved everything. She asked about each of you by name ❤️ Thank you for coming")
	c.say(M, "").attach(b.photo(480, 320, sceneDusk, "garden.png", ""), b.art(320, 320, patternWaves, 0xfff3b0, 0xe09f3e, "flowers.png"))
	c.say(J, "Oma beat me at Rommé three times. I'm not ok")
	c.say(D, "She has been playing since 1953.")

	c.day(0, 9, 40)
	c.say(M, "Who wants the leftover cake?? Otherwise Papa eats it all again 🍰")
	c.say(J, "ME")
	c.say(D, "Too late.")
	c.unreadTail(3)
}

func (s *Service) seedWork(b *builder) {
	const me = SelfACI
	D, G, O := DevACI, GraceACI, OskarACI
	cs := s.group(ChatWork, "Platform Team",
		"Deploys, incidents, and the occasional cake. Runbooks: wiki/platform",
		[]core.ACI{D, G, O}, D, G)
	cs.chat.MutedUntil = time.Date(b.now.Year(), b.now.Month(), b.now.Day()+1, 9, 0, 0, 0, b.now.Location())
	c := b.convo(cs)

	c.day(20, 9, 30)
	c.say(G, "Welcome to the team, @{"+string(me)+"}! 🎉 Dev is your onboarding buddy, ask him anything. Except about the 2019 migration. Never ask about the 2019 migration.")
	c.say(D, "we don't talk about the 2019 migration")
	c.say(O, "välkommen! 👋")
	c.me("thanks all! happy to be here. I've read the glossary of the glossary")
	c.say(D, "you're already further than most")

	c.day(18, 14, 2)
	c.say(D, "PSA: staging is down, someone ran the cleanup job with `--all` instead of `--expired`")
	c.say(O, "…was it me")
	c.say(D, "it was you")
	c.say(O, "🫠")
	c.say(G, "no blame! but let's make `--all` require a confirmation flag. Oskar can you open a ticket")
	c.say(O, "PLAT-2291, on it")

	c.day(15, 10, 0)
	c.say(G, "Roadmap for Q4 attached. Highlights:\n\n1. Move the job queue off the old Redis cluster\n2. Cut p99 deploy time below 6 minutes\n3. Kill the last cron box 💀").
		attach(b.store("file", "application/pdf", "Platform-Q4-Roadmap.pdf",
			pdfDocument("Platform Q4 Roadmap", []string{
				"1. Migrate job queue off redis-legacy (owner: Dev)",
				"2. Deploy p99 < 6 min (owner: Robin)",
				"3. Decommission cron-01 (owner: Oskar)",
				"4. Incident review cadence: biweekly",
			})))
	c.say(D, "item 3 makes me emotional. cron-01 has been up for 1,612 days")
	c.say(O, "we should give it a funeral")
	c.say(G, "approved. cake budget allocated")

	c.day(13, 16, 45)
	c.me("first PR is up! small one: caches the dependency layer in CI, should shave ~90s off every build\n\nhttps://github.com/example/platform/pull/1842").link(core.LinkPreview{
		URL:         "https://github.com/example/platform/pull/1842",
		Title:       "ci: cache dependency layer between builds by robinw · Pull Request #1842",
		Description: "Reuses the module cache across runs. Median build 4m12s → 2m38s.",
		Image:       ptr(b.art(240, 120, patternChecker, 0xf6f8fa, 0xd0d7de, "pr.png")),
	})
	c.say(D, "reviewing now 👀")
	c.say(D, "lgtm, one nit about the cache key. ship it 🚢").react(me, "🙏", G, "🚀")
	c.say(G, "90s × 400 builds a day = 10 hours a day. Robin just paid for themselves")

	c.day(11, 3, 12)
	c.say(D, "🚨 **INC-77**: queue latency spiking, p99 at 40s. I'm on it")
	c.say(D, "looks like a hot partition. rebalancing")
	c.say(D, "ok recovered. post-mortem tomorrow. going back to sleep 😴")
	c.day(11, 9, 3)
	c.say(G, "thanks for handling that Dev 🙏 take the morning")
	c.say(O, "hero")
	c.say(D, "I'll take the morning and also the leftover cake")

	c.day(9, 11, 20)
	c.say(O, "can someone explain why this works:\n```\nfor i := range 10 {\n    go func() { fmt.Println(i) }()\n}\n```\nI was taught this was a bug")
	c.me("since Go 1.22 the loop variable is per-iteration, so each goroutine gets its own `i`. it was a bug before that")
	c.say(O, "TIL. thanks Robin!").react(D, "💯")

	c.day(7, 13, 0)
	poll := c.me("")
	poll.m.Poll = &core.Poll{
		Question: "Retro format for Friday?",
		Options: []core.PollOption{
			{Text: "Start / Stop / Continue", Voters: []core.ACI{G}},
			{Text: "Sailboat ⛵", Voters: []core.ACI{O, me}},
			{Text: "Just vibes", Voters: []core.ACI{D}},
		},
	}
	c.say(D, "\"just vibes\" is a valid retro format and I will die on this hill")

	c.day(5, 10, 30)
	c.say(G, "**On-call rotation (Oct/Nov)**\n• wk 42 — Dev\n• wk 43 — Robin (shadowing Dev)\n• wk 44 — Oskar\n• wk 45 — Grace\n\nSwaps: just post here and update PagerDuty.").pin()
	c.say(O, "👍")
	c.me("ready to be shadowed 🫡")

	c.day(3, 15, 15)
	c.say(D, "deploy p99 this week: 7m40s → 6m55s. getting there")
	c.say(G, "nice! what moved it?")
	c.say(D, "Robin's cache PR plus parallelising the smoke tests").
		edited("Robin's cache PR")
	c.me("team effort 💪")

	c.day(1, 16, 50)
	c.say(O, "cron-01 funeral is Thursday 4pm, kitchen. I made a eulogy").react(G, "😂", D, "🪦")
	c.say(G, "dress code: black. or a hoodie, it's a platform team")

	c.day(0, 9, 5)
	c.say(D, "standup in 5, I'll be 2 min late, coffee machine is doing the thing again")
	c.say(G, "ok, @{"+string(me)+"} can you kick it off?")
	c.say(O, "the coffee machine needs its own on-call rotation")
	c.unreadTail(3)
}

func (s *Service) seedTrailAlerts(b *builder) {
	R := RangerACI
	cs := s.group(ChatTrailAlerts, "Trail Conditions · Wartberg",
		"Official notices from the Bergwacht. Only admins can post. Emergency: 112.",
		[]core.ACI{R, ElifACI, AliceACI, BobACI}, R)
	cs.chat.OnlyAdminsSend = true
	cs.chat.AddFromLinkMode = core.AccessAny
	cs.chat.InviteLink = "https://signal.group/#CjQKIGJlcmd3YWNodC10cmFpbC1hbGVydHMtZGVtbxIQ"
	c := b.convo(cs)

	c.day(23, 8, 0)
	c.sys("You joined the group via the group link.")
	c.say(R, "**Welcome!** This group shares trail closures, weather warnings and hut opening times for the Wartberg area. Replies are disabled — for questions, contact the visitor centre.")
	c.day(19, 7, 30)
	c.say(R, "⚠️ **Closure:** the Klammweg section between km 3 and 4.5 is closed due to rockfall. Diversion via the Forstweg is signposted (+1.2 km).")
	c.day(16, 16, 0)
	c.say(R, "Hohe Warte tower: open daily 8:00–18:00 until Oct 31, then weekends only. The tower gate opens at sunrise on Saturdays this month 🌅")
	c.day(12, 6, 45)
	c.say(R, "🌬️ **Wind warning** for the ridge today: gusts up to 70 km/h above 600 m. The wooden tower at Rosenwarte is closed until further notice.")
	c.day(9, 12, 0)
	c.say(R, "Hut update: Wartberghütte is cash only until the card reader is fixed. Kaiserschmarrn remains available 😉")
	c.day(6, 6, 30)
	c.say(R, "Clear skies and frost this morning (-2°C at the summit). Paths in the forest are icy in places — take care on the roots below the tower.")
	c.day(2, 9, 0)
	c.say(R, "✅ Klammweg is **open again**. Thanks to the volunteers who cleared the rockfall over the weekend!").
		react(ElifACI, "🙏", AliceACI, "❤️")
	c.day(0, 7, 15)
	c.say(R, "First snow forecast above 800 m from Wednesday. Rosenwarte remains closed (wind damage inspection). Please check before you go.")
	c.unreadTail(1)
}

func (s *Service) seedDev(b *builder) {
	const me = SelfACI
	D := DevACI
	cs := s.direct(D)
	c := b.convo(cs)

	c.day(20, 11, 0)
	c.say(D, "hey! I'm your onboarding buddy. no question is dumb. except asking about the 2019 migration")
	c.me("noted. what's the 2019 migration")
	c.say(D, "…")
	c.say(D, "it was a dark time")
	c.day(19, 15, 30)
	c.me("where do the deploy logs live? the wiki link 404s")
	c.say(D, "wiki is lying. it's `kubectl logs -n deploy -l app=shipper --since=1h`, or the Grafana board \"Shipper\"")
	c.me("thanks!! adding that to my notes")
	c.day(17, 9, 45)
	c.say(D, "coffee? the good machine on 3 is working today, which is a rare astronomical event")
	c.me("omw ☕")
	c.day(14, 17, 10)
	c.me("is it normal that `make test` takes 11 minutes")
	c.say(D, "yes and I hate it. use `make test-fast` for the inner loop, it skips the integration suite")
	c.say(D, "also hi, that's literally the thing your PR is going to fix 😄")
	c.day(11, 10, 30)
	c.call(me, core.CallInfo{Video: true, Duration: 18 * time.Minute})
	c.say(D, "thanks for pairing, that rebalancing script is way cleaner now")
	c.me("thanks for not judging my vim config")
	c.say(D, "I judged it a little")
	c.day(8, 12, 0)
	c.say(D, "you're shadowing me on call wk 43. don't worry, it's usually quiet")
	c.me("famous last words")
	c.say(D, "I said usually")
	c.day(4, 18, 20)
	c.call(D, core.CallInfo{Declined: true})
	c.say(D, "sorry, wrong Robin")
	c.me("there's another Robin??")
	c.say(D, "Robin from finance. you're Robin from platform now. it's a title")
	c.day(0, 10, 2)
	c.me("pushed the fix for the flaky test, can you rerun CI?")
	c.me("hm, that one didn't go through, retrying").status(core.StatusFailed)
}

func (s *Service) seedMom(b *builder) {
	const me = SelfACI
	M := MomACI
	cs := s.direct(M)
	c := b.convo(cs)

	c.day(25, 19, 0)
	c.say(M, "Hello my darling, how is the new job? Are you eating properly?")
	c.me("It's good Mama! Busy, but nice people. And yes, I'm eating")
	c.say(M, "Vegetables?")
	c.me("…define vegetables")
	c.say(M, "😤")
	c.day(21, 12, 30)
	c.call(M, core.CallInfo{Duration: 12 * time.Minute})
	c.say(M, "Thank you for calling back! Don't forget Oma's birthday")
	c.me("I won't! It's in my calendar with 3 reminders")
	c.day(15, 18, 10)
	c.say(M, "").attach(b.photo(320, 240, sceneLake, "walk.png", "Our walk today at the lake! Papa insisted on the long way 🙄"))
	c.me("so pretty! Papa was right for once")
	c.say(M, "Don't tell him")
	c.day(10, 20, 5)
	c.say(M, "Did you see the article I sent about vitamin D? In winter you need it!")
	c.me("I did. I'll buy some, promise")
	c.say(M, "I already ordered you some. They arrive Tuesday ❤️")
	c.me("of course you did 😂❤️")
	c.day(6, 21, 30)
	c.say(M, "Jonas told me you went up a tower at 5 in the morning?? In the dark?? Be careful!!")
	c.me("We had headlamps and Elif! It was beautiful, I'll show you the photos on Sunday")
	c.say(M, "Elif is a sensible girl. OK. 😌")
	c.day(2, 11, 15)
	c.call(M, core.CallInfo{Missed: true})
	c.say(M, "Call me back when you have a minute, nothing urgent!")
	c.me("in a meeting, will call tonight 💛")
	c.say(M, "👍")
	c.day(0, 8, 0)
	c.say(M, "Good morning! Wear a jacket today, it's cold 🧥")
	c.unreadTail(1)
}

func (s *Service) seedFelix(b *builder) {
	const me = SelfACI
	F := FelixACI
	cs := s.direct(F)
	cs.chat.MutedUntil = muteForever
	c := b.convo(cs)

	c.day(14, 19, 0)
	c.say(F, "you asked about the starter. here she is. her name is Doughlores").
		attach(b.art(240, 320, patternCircles, 0xfaf3e0, 0xc9ada7, "starter.png"))
	c.me("Doughlores 😂 she's beautiful")
	c.say(F, "she's 6 years old and has outlived two flatmates")
	c.day(10, 8, 20)
	c.say(F, "want some of her? I can split off 100g")
	c.me("yes please! is she high maintenance")
	c.say(F, "feed her once a week, tell her she's pretty")
	c.sticker(me, 3)
	c.day(3, 13, 0)
	c.say(F, "loaf report 🍞").attach(b.art(400, 300, patternWaves, 0xe9c46a, 0x8a5a44, "loaf.png"))
	c.say(F, "open crumb. ear like a cliff. I peaked today")
	c.me("that's art. frame it. then eat it")
	c.day(0, 7, 50)
	c.say(F, "baking cardamom buns for the next hike as a test run. you are contractually obligated to taste")
	c.say(F, "this is not optional")
	c.unreadTail(2)
}

func (s *Service) seedNoor(b *builder) {
	const me = SelfACI
	N := NoorACI
	cs := s.direct(N)
	c := b.convo(cs)

	c.day(5, 20, 0)
	c.say(N, "hey! switching this chat to disappearing messages, hope that's ok. I do it everywhere now")
	c.me("totally fine")
	cs.chat.ExpireTimer = 7 * 24 * time.Hour
	c.sys("Noor set the disappearing message timer to 1 week.")
	c.say(N, "ty! so. did you finish the thing")
	c.me("which thing")
	c.say(N, "the THING. the side project. the one with the towers")
	c.me("ohh. almost. it's a terminal app now. don't laugh")
	c.say(N, "I would never laugh at a terminal app. I live in tmux")
	c.day(4, 22, 10)
	opened := c.say(N, "")
	opened.m.ViewOnce, opened.m.ViewOnceOpened = true, true
	c.me("ok that was hilarious. I can't believe it vanished")
	c.say(N, "that's the beauty of it")
	c.day(1, 19, 30)
	c.say(N, "concert next friday? the post-rock band with the 14 minute songs")
	c.me("YES. I'll bring earplugs and patience")
	c.day(0, 11, 0)
	vo := c.say(N, "")
	vo.m.ViewOnce = true
	vo.attach(b.photo(320, 480, sceneNight, "secret.png", ""))
	c.say(N, "open it, it's the tickets 🎟️")
	c.unreadTail(2)
}

func (s *Service) seedJonas(b *builder) {
	const me = SelfACI
	J := JonasACI
	cs := s.direct(J)
	c := b.convo(cs)

	c.day(18, 23, 40)
	c.say(J, "quick question, totally unrelated to anything, how much does it cost to replace a car mirror")
	c.me("JONAS")
	c.say(J, "it's fine it's fine. it's mostly fine")
	c.sticker(J, 1)
	c.day(16, 10, 0)
	c.say(J, "mama's cat is sick, she asked for the vet's number for you, here").react(me, "🙏")
	c.lastMsg().m.Body = ""
	c.lastMsg().m.Contact = &core.SharedContact{
		Name:   "Dr. Anna Weiss (Tierarzt)",
		Phones: []string{"+49 30 1234567", "+49 170 7654321"},
		Emails: []string{"praxis@weiss-tierarzt.example"},
		Org:    "Tierarztpraxis Weiss",
	}
	c.me("thanks! called her, Minka is fine, just ate a hair tie")
	c.say(J, "classic Minka")
	c.day(12, 15, 20)
	c.say(J, "can you proofread my cover letter. the job is \"barista\" so the bar is low but")
	c.me("send it")
	c.say(J, "").attach(b.store("file", "application/pdf", "cover-letter-final-FINAL-v3.pdf",
		pdfDocument("Cover Letter", []string{"Dear Café Lindenblatt team,", "", "I am passionate about coffee and also about people.", "Mostly coffee."})))
	c.me("\"mostly coffee\" stays. it's honest")
	c.day(7, 21, 0)
	gif := c.say(J, "me at the oral exam tomorrow").attach(b.store("gif", "image/gif", "panic.gif", signalFireGIF()))
	gif.m.Attachments[0].GIF = true
	c.me("you'll be fine!! you know this stuff")
	c.sticker(me, 7)
	c.day(6, 14, 0)
	c.say(J, "PASSED 🎓🎓🎓").react(me, "🎉")
	c.me("TOLD YOU")
	c.day(1, 22, 30)
	c.say(J, "lend me €20 till friday? the barista job pays on friday")
	c.me("sent. you're buying me a flat white")
	c.say(J, "deal 🤝")
}

func (s *Service) seedMarco(b *builder) {
	const me = SelfACI
	M := MarcoACI
	cs := s.direct(M)
	cs.chat.Archived = true
	c := b.convo(cs)

	c.day(26, 18, 0)
	c.say(M, "Ciao! Are you still coming to Torino in the spring?")
	c.me("That's the plan! April probably")
	c.say(M, "Perfect. I'll show you the real bicerin place, not the tourist one ☕")
	c.say(M, "").attach(notDownloaded(b.photo(400, 260, sceneDusk, "torino.png", "Mole Antonelliana at sunset")))
	c.me("ok I'm booking flights tonight")
	c.say(M, "🇮🇹🙌")
	c.day(24, 9, 0)
	c.me("booked! 12–19 April")
	c.say(M, "Grande! I'll find us a watchtower too, I heard you collect them now 😄")
}

func (s *Service) seedInes(b *builder) {
	I := InesACI
	cs := s.direct(I)
	cs.chat.MessageRequest = true
	c := b.convo(cs)
	c.day(1, 18, 20)
	c.say(I, "Hi Robin! Elif gave me your username — hope that's ok.")
	c.say(I, "We're organising a bouldering + watchtower weekend in November with the climbing gym crowd. Elif says you bring a legendary thermos? Would love to have you along 🧗")
	c.unreadTail(2)
}

func (s *Service) seedMax(b *builder) {
	X := MaxACI
	cs := s.direct(X)
	c := b.convo(cs)
	c.day(9, 3, 14)
	c.say(X, "Hello dear friend!! 💸💸 I have an EXCLUSIVE opportunity for you")
	c.say(X, "Invest 250€ today, receive 2500€ by Friday GUARANTEED 📈🚀")
	c.say(X, "Reply YES to receive the link")
}

func (s *Service) seedNoteToSelf(b *builder) {
	cs := s.direct(SelfACI)
	cs.chat.Pinned, cs.chat.PinOrder = true, 2
	c := b.convo(cs)
	c.day(27, 22, 0)
	c.me("**ideas for warte**\n• `:search from:alice has:image`\n• fire-signal animation when a message arrives\n• tower-to-tower relay = the name!")
	c.me("buy: headlamp batteries, gloves, the big thermos lid (it leaks)")
	c.day(21, 8, 30)
	c.me("wifi at the office: `platform-guest` / ~~hunter2~~ `Correct-Horse-7`").edited("wifi at the office: `platform-guest` / `hunter2`")
	c.me("https://go.dev/blog/loopvar-preview").link(core.LinkPreview{
		URL:         "https://go.dev/blog/loopvar-preview",
		Title:       "Fixing For Loops in Go 1.22 - The Go Programming Language",
		Description: "Go 1.22 changes the semantics of for loop variables to be per-iteration.",
	})
	c.day(15, 19, 30)
	c.me("").attach(withVoice(b.store("voice", "audio/ogg", "", voiceNoteOgg(9))))
	c.me("^ the melody that was stuck in my head on the train, figure out what it is")
	c.day(9, 12, 0)
	c.me("books to read:\n1. A Field Guide to Leaving\n2. Small Hours of the Fen\n3. that one about lighthouses Clara mentioned")
	c.day(5, 21, 0)
	c.me("").attach(b.photo(640, 240, sceneNight, "stars.png", "the night sky from the tower parking lot. 5:40am"))
	c.day(1, 23, 10)
	c.me("Rosenwarte sauna — call Mon–Fri 9–17, +49 7654 32100, ask for the \"Turmzimmer\" slot")
	c.day(0, 7, 30)
	c.me("today: 1) PR review 2) book sauna 3) call Mama 4) mandarin for Bob 🍊")
}

func (s *Service) seedStories(b *builder) {
	add := func(st *core.Story) {
		if st.ID.Chat == "" {
			st.ID.Chat = core.DirectChatID(st.ID.Author)
		}
		s.stories = append(s.stories, st)
	}
	now := b.now
	add(&core.Story{
		ID: core.MessageID{Author: AliceACI}, At: now.Add(-50 * time.Minute),
		Text: &core.TextStory{Text: "first snow on the ridge ❄️", Style: 1, Foreground: 0xffffffff,
			Background: 0xff3d5a80, Gradient: []uint32{0xff3d5a80, 0xff98c1d9}},
	})
	sunrise := b.photo(270, 480, sceneSunrise, "story-sunrise.png", "")
	add(&core.Story{
		ID: core.MessageID{Author: BobACI}, At: now.Add(-3 * time.Hour),
		Media: &sunrise, Caption: "5:47am. worth it. (don't tell Elif)",
	})
	lake := b.photo(270, 480, sceneLake, "story-lake.png", "")
	add(&core.Story{
		ID: core.MessageID{Author: ElifACI, Chat: ChatHike}, At: now.Add(-5 * time.Hour),
		Media: &lake, Caption: "scouting Rosenwarte 👀", Group: ChatHike, Replies: 3,
	})
	add(&core.Story{
		ID: core.MessageID{Author: MomACI}, At: now.Add(-9 * time.Hour), Viewed: true,
		Text: &core.TextStory{Text: "Happy Sunday everyone! 🌻", Style: 3, Foreground: 0xff2b2d42, Background: 0xffffd166},
	})
	cover := b.art(270, 480, patternStripes, 0xf2e8cf, 0x6a994e, "story-book.png")
	add(&core.Story{
		ID: core.MessageID{Author: ClaraACI}, At: now.Add(-14 * time.Hour), Viewed: true,
		Media: &cover, Caption: "November pick has arrived 📚",
	})
	add(&core.Story{
		ID: core.MessageID{Author: SelfACI}, At: now.Add(-7 * time.Hour), Viewed: true, Replies: 2,
		Text: &core.TextStory{Text: "warte: tower to tower, ridge to ridge 🔥", Style: 2, Foreground: 0xffffffff,
			Background: 0xff1d1147, Gradient: []uint32{0xff1d1147, 0xffff6f91}, URL: "https://en.wikipedia.org/wiki/Warte"},
	})
}

// lastMsg returns the most recently scripted message.
func (c *convo) lastMsg() *msg {
	return &msg{c: c, m: c.cs.msgs[len(c.cs.msgs)-1]}
}

func ptr[T any](v T) *T { return &v }

func withVoice(a core.Attachment) core.Attachment {
	a.VoiceNote = true
	return a
}

func withSize(a core.Attachment, w, h int) core.Attachment {
	a.Width, a.Height = w, h
	return a
}
