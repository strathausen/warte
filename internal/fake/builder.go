package fake

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif" // register decoders for DecodeConfig/blurhash
	_ "image/png"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"warte/internal/core"
)

// builder assembles the demo data. Conversations are written as scripts with
// a lightweight markup (see parseMarkup) on a wall-clock calendar relative to
// "now"; finalize then shifts everything into the past if needed, assigns
// unique timestamps and resolves quotes.
type builder struct {
	s      *Service
	now    time.Time
	attSeq int
	convos []*convo
	quotes []pendingQuote
}

type pendingQuote struct {
	m, target *core.Message
}

// convo scripts one chat.
type convo struct {
	b      *builder
	cs     *chatState
	cur    time.Time
	unread int
	peers  []core.ACI // recipients of outgoing messages (excluding self)
	meta   map[*core.Message]*msgMeta
}

type msgMeta struct {
	explicitStatus bool
	expireAfter    time.Duration // ExpireStartedAt = SentAt + this
}

// msg is a fluent handle on a scripted message.
type msg struct {
	c *convo
	m *core.Message
}

func (b *builder) convo(cs *chatState) *convo {
	c := &convo{b: b, cs: cs, cur: b.now.AddDate(0, 0, -30), meta: map[*core.Message]*msgMeta{}}
	for _, m := range cs.chat.Members {
		if m.ACI != b.s.self.ACI {
			c.peers = append(c.peers, m.ACI)
		}
	}
	if aci := cs.chat.ID.ACI(); aci != "" && aci != b.s.self.ACI {
		c.peers = []core.ACI{aci}
	}
	b.convos = append(b.convos, c)
	return c
}

// day moves the script cursor to hh:mm on the day daysAgo days before now.
func (c *convo) day(daysAgo, hh, mm int) *convo {
	n := c.b.now
	c.cur = time.Date(n.Year(), n.Month(), n.Day()-daysAgo, hh, mm, c.b.s.rng.IntN(50), 0, n.Location())
	return c
}

// wait advances the cursor by the given number of minutes.
func (c *convo) wait(minutes int) *convo {
	c.cur = c.cur.Add(time.Duration(minutes) * time.Minute)
	return c
}

func (c *convo) advance() {
	// Conversations flow in bursts: mostly quick replies, sometimes a pause.
	r := c.b.s.rng
	var d time.Duration
	switch x := r.IntN(10); {
	case x < 6:
		d = time.Duration(15+r.IntN(90)) * time.Second
	case x < 9:
		d = time.Duration(2+r.IntN(6)) * time.Minute
	default:
		d = time.Duration(10+r.IntN(40)) * time.Minute
	}
	c.cur = c.cur.Add(d)
}

func (c *convo) add(m *core.Message) *msg {
	c.cs.msgs = append(c.cs.msgs, m)
	c.meta[m] = &msgMeta{}
	return &msg{c: c, m: m}
}

// say scripts an incoming (or, for self, outgoing) message with markup.
func (c *convo) say(who core.ACI, text string) *msg {
	c.advance()
	body, ranges, mentionsSelf := parseMarkup(text, c.b.s.self.ACI)
	out := who == c.b.s.self.ACI
	m := &core.Message{
		ID:           core.MessageID{Chat: c.cs.chat.ID, Author: who},
		Kind:         core.KindNormal,
		Outgoing:     out,
		SentAt:       c.cur,
		Body:         body,
		Ranges:       ranges,
		MentionsSelf: mentionsSelf && !out,
		ExpiresIn:    c.cs.chat.ExpireTimer,
	}
	if c.cs.chat.ExpireTimer > 0 {
		c.add(m)
		c.meta[m].expireAfter = time.Duration(1+c.b.s.rng.IntN(20)) * time.Minute
		return &msg{c: c, m: m}
	}
	return c.add(m)
}

// me scripts an outgoing message.
func (c *convo) me(text string) *msg { return c.say(c.b.s.self.ACI, text) }

// sys scripts a system notice.
func (c *convo) sys(text string) *msg {
	c.advance()
	return c.add(&core.Message{
		ID:         core.MessageID{Chat: c.cs.chat.ID, Author: ""},
		Kind:       core.KindSystem,
		SentAt:     c.cur,
		SystemText: text,
	})
}

// call scripts a call event. from is the caller (self for outgoing).
func (c *convo) call(from core.ACI, info core.CallInfo) *msg {
	c.advance()
	info.Incoming = from != c.b.s.self.ACI
	info.StartedAt = c.cur
	m := c.add(&core.Message{
		ID:       core.MessageID{Chat: c.cs.chat.ID, Author: from},
		Kind:     core.KindCall,
		Outgoing: !info.Incoming,
		SentAt:   c.cur,
		Call:     &info,
	})
	return m
}

// reply quotes another scripted message.
func (m *msg) reply(to *msg) *msg {
	m.c.b.quotes = append(m.c.b.quotes, pendingQuote{m: m.m, target: to.m})
	return m
}

// react adds reactions: pairs of (who, emoji).
func (m *msg) react(pairs ...any) *msg {
	for i := 0; i+1 < len(pairs); i += 2 {
		m.m.Reactions = append(m.m.Reactions, core.Reaction{
			From:  pairs[i].(core.ACI),
			Emoji: pairs[i+1].(string),
			At:    m.m.SentAt.Add(time.Duration(2+i) * time.Minute),
		})
	}
	return m
}

// edited records earlier revisions (oldest first, each in markup).
func (m *msg) edited(revisions ...string) *msg {
	for i, r := range revisions {
		body, ranges, _ := parseMarkup(r, m.c.b.s.self.ACI)
		m.m.EditHistory = append(m.m.EditHistory, core.EditRevision{
			Body: body, Ranges: ranges, At: m.m.SentAt.Add(time.Duration(i) * 40 * time.Second),
		})
	}
	m.m.Edited = true
	return m
}

func (m *msg) attach(as ...core.Attachment) *msg {
	m.m.Attachments = append(m.m.Attachments, as...)
	return m
}

func (m *msg) link(p core.LinkPreview) *msg {
	m.m.Preview = &p
	return m
}

func (m *msg) status(st core.DeliveryStatus) *msg {
	m.m.Status = st
	m.c.meta[m.m].explicitStatus = true
	return m
}

func (m *msg) pin() *msg {
	m.m.Pinned = true
	return m
}

func (m *msg) deleted() *msg {
	m.m.RemoteDeleted = true
	m.m.Body, m.m.Ranges, m.m.Attachments, m.m.Reactions = "", nil, nil, nil
	return m
}

// unreadTail marks the last n unread-candidate messages as unread.
func (c *convo) unreadTail(n int) *convo {
	c.unread = n
	return c
}

// ---------------------------------------------------------------------------
// Media helpers

func (b *builder) nextAttID(kind string) string {
	b.attSeq++
	return fmt.Sprintf("%s-%04d", kind, b.attSeq)
}

func (b *builder) store(kind, contentType, name string, data []byte) core.Attachment {
	id := b.nextAttID(kind)
	b.s.blobs[id] = data
	a := core.Attachment{
		ID:          id,
		ContentType: contentType,
		FileName:    name,
		Size:        int64(len(data)),
		Downloaded:  true,
		Progress:    1,
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		a.Width, a.Height = cfg.Width, cfg.Height
		a.BlurHash = blurHash(data)
	}
	return a
}

func (b *builder) photo(w, h, variant int, name, caption string) core.Attachment {
	a := b.store("img", "image/png", name, landscapePNG(w, h, variant))
	a.Caption = caption
	return a
}

func (b *builder) art(w, h, variant int, c1, c2 uint32, name string) core.Attachment {
	return b.store("img", "image/png", name, patternPNG(w, h, variant, c1, c2))
}

// notDownloaded marks an attachment as not yet fetched; Service.Attachment
// simulates the download.
func notDownloaded(a core.Attachment) core.Attachment {
	a.Downloaded, a.Progress = false, 0
	return a
}

// ---------------------------------------------------------------------------
// finalize

// finalize shifts the timeline into the past, assigns unique timestamps,
// derives statuses and read markers, and resolves quotes.
func (b *builder) finalize() {
	latest := time.Time{}
	for _, c := range b.convos {
		for _, m := range c.cs.msgs {
			if m.SentAt.After(latest) {
				latest = m.SentAt
			}
		}
	}
	limit := b.now.Add(-90 * time.Second)
	var shift time.Duration
	if latest.After(limit) {
		shift = latest.Sub(limit)
	}
	for _, c := range b.convos {
		slices.SortStableFunc(c.cs.msgs, func(x, y *core.Message) int { return x.SentAt.Compare(y.SentAt) })
		var prev uint64
		for _, m := range c.cs.msgs {
			at := m.SentAt.Add(-shift)
			ts := uint64(at.UnixMilli())
			if ts <= prev {
				ts = prev + 1
			}
			prev = ts
			at = time.UnixMilli(int64(ts)).In(b.now.Location())
			delta := at.Sub(m.SentAt)
			m.SentAt = at
			m.ID.Timestamp = ts
			m.ServerTimestamp = ts + 120
			if m.Outgoing {
				m.ReceivedAt = at
			} else {
				m.ReceivedAt = at.Add(time.Duration(300+int(ts%1700)) * time.Millisecond)
			}
			for i := range m.Reactions {
				m.Reactions[i].At = m.Reactions[i].At.Add(delta)
			}
			for i := range m.EditHistory {
				m.EditHistory[i].At = m.EditHistory[i].At.Add(delta)
			}
			if m.Call != nil {
				m.Call.StartedAt = at
			}
			meta := c.meta[m]
			if meta != nil && m.ExpiresIn > 0 {
				m.ExpireStartedAt = at.Add(meta.expireAfter)
				if m.Outgoing {
					m.ExpireStartedAt = at
				}
			}
			if m.Outgoing && m.Kind == core.KindNormal {
				b.deriveStatus(c, m, meta != nil && meta.explicitStatus)
			}
			if prev > b.s.lastTS {
				b.s.lastTS = prev
			}
		}
		if len(c.cs.msgs) > 0 {
			c.cs.created = c.cs.msgs[0].SentAt
		} else {
			c.cs.created = b.now.Add(-shift - 72*time.Hour)
		}
		c.cs.readUpTo = readMarker(c.cs.msgs, c.unread)
		for _, m := range c.cs.msgs {
			// Disappearing timers of incoming messages start when read.
			if !m.Outgoing && m.ID.Timestamp > c.cs.readUpTo {
				m.ExpireStartedAt = time.Time{}
			}
		}
	}
	for _, q := range b.quotes {
		t := q.target
		qt := &core.Quote{
			ID:            t.ID,
			AuthorName:    b.s.nameLocked(t.ID.Author),
			Text:          t.Body,
			Ranges:        cloneRanges(t.Ranges),
			OriginalFound: true,
		}
		if len(t.Attachments) > 0 {
			qt.Attachment = cloneAttachment(&t.Attachments[0])
		}
		q.m.Quote = qt
	}
	for _, s := range b.s.stories {
		s.At = s.At.Add(-shift)
		s.ID.Timestamp = uint64(s.At.UnixMilli())
	}
}

func (b *builder) deriveStatus(c *convo, m *core.Message, explicit bool) {
	if !explicit {
		m.Status = core.StatusRead
		if c.cs.chat.Kind == core.ChatNoteToSelf {
			m.Status = core.StatusSent
		}
	}
	if c.cs.chat.Kind != core.ChatGroup {
		return
	}
	m.PerRecipient = map[core.ACI]core.DeliveryStatus{}
	for i, p := range c.peers {
		st := m.Status
		// Groups are rarely uniformly read: let one member lag behind.
		if st == core.StatusRead && i == int(m.ID.Timestamp%uint64(max(1, len(c.peers)))) && m.ID.Timestamp%3 == 0 {
			st = core.StatusDelivered
		}
		m.PerRecipient[p] = st
	}
}

// readMarker returns the timestamp up to which messages are read such that
// exactly unread unread-candidate messages remain.
func readMarker(msgs []*core.Message, unread int) uint64 {
	if len(msgs) == 0 {
		return 0
	}
	if unread <= 0 {
		return msgs[len(msgs)-1].ID.Timestamp
	}
	n := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if isUnreadCandidate(msgs[i]) {
			n++
			if n == unread {
				return msgs[i].ID.Timestamp - 1
			}
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Markup

// parseMarkup converts demo markup into a Signal body and UTF-16 ranges:
//
//	**bold**  __italic__  ~~strike~~  `mono`  ||spoiler||  @{<aci>}
//
// Mentions become U+FFFC with a StyleMention range of length 1. It reports
// whether self was mentioned.
func parseMarkup(text string, self core.ACI) (string, []core.BodyRange, bool) {
	type delim struct {
		tok   string
		style core.RangeStyle
	}
	delims := []delim{
		{"**", core.StyleBold}, {"__", core.StyleItalic}, {"~~", core.StyleStrikethrough},
		{"||", core.StyleSpoiler}, {"`", core.StyleMonospace},
	}
	var (
		out          strings.Builder
		ranges       []core.BodyRange
		pos          int
		open         = map[core.RangeStyle]int{}
		mentionsSelf bool
	)
outer:
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], "@{") {
			if end := strings.IndexByte(text[i:], '}'); end > 0 {
				aci := core.ACI(text[i+2 : i+end])
				out.WriteRune('￼')
				ranges = append(ranges, core.BodyRange{Start: pos, Length: 1, Style: core.StyleMention, MentionACI: aci})
				if aci == self {
					mentionsSelf = true
				}
				pos++
				i += end + 1
				continue
			}
		}
		for _, d := range delims {
			if !strings.HasPrefix(text[i:], d.tok) {
				continue
			}
			if start, ok := open[d.style]; ok {
				if pos > start {
					ranges = append(ranges, core.BodyRange{Start: start, Length: pos - start, Style: d.style})
				}
				delete(open, d.style)
			} else {
				open[d.style] = pos
			}
			i += len(d.tok)
			continue outer
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		out.WriteRune(r)
		pos += utf16Len(string(r))
		i += size
	}
	slices.SortStableFunc(ranges, func(a, b core.BodyRange) int { return a.Start - b.Start })
	return out.String(), ranges, mentionsSelf
}

// ---------------------------------------------------------------------------
// Identifiers

// groupIDFor derives a stable base64 group identifier from a name.
func groupIDFor(name string) string {
	sum := sha256.Sum256([]byte("warte-group/" + name))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func hexFor(name string, n int) string {
	sum := sha256.Sum256([]byte("warte/" + name))
	h := hex.EncodeToString(sum[:])
	for len(h) < n {
		h += h
	}
	return h[:n]
}

// ---------------------------------------------------------------------------
// BlurHash

const blurHashChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~"

func base83(v, length int) string {
	out := make([]byte, length)
	for i := 1; i <= length; i++ {
		d := (v / int(math.Pow(83, float64(length-i)))) % 83
		out[i-1] = blurHashChars[d]
	}
	return string(out)
}

// srgbLinear is srgbToLinear for every byte value.
var srgbLinear = func() (t [256]float64) {
	for i := range t {
		t[i] = srgbToLinear(uint8(i))
	}
	return t
}()

func srgbToLinear(v uint8) float64 {
	x := float64(v) / 255
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

func linearToSRGB(v float64) int {
	v = math.Max(0, math.Min(1, v))
	if v <= 0.0031308 {
		return int(v*12.92*255 + 0.5)
	}
	return int((1.055*math.Pow(v, 1/2.4)-0.055)*255 + 0.5)
}

func signPow(v, exp float64) float64 {
	return math.Copysign(math.Pow(math.Abs(v), exp), v)
}

// blurHash encodes an image with 4x3 components (https://blurha.sh). Returns
// "" if the data can't be decoded.
func blurHash(data []byte) string {
	return string(memo("blurhash/"+hexOf(data), func() []byte {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		const cx, cy = 4, 3
		bnd := img.Bounds()
		w, h := bnd.Dx(), bnd.Dy()
		step := max(1, min(w, h)/48)
		// Sample the image once into linear RGB and precompute the cosine
		// bases; the sums below run in the same order as the textbook
		// per-pixel loop, so the hash is unchanged, but demo startup no
		// longer spends most of its time in math.Pow and image.At.
		var xs, ys []int
		for x := 0; x < w; x += step {
			xs = append(xs, x)
		}
		for y := 0; y < h; y += step {
			ys = append(ys, y)
		}
		lin := make([][3]float64, 0, len(xs)*len(ys))
		for _, y := range ys {
			for _, x := range xs {
				pr, pg, pb, _ := img.At(bnd.Min.X+x, bnd.Min.Y+y).RGBA()
				lin = append(lin, [3]float64{srgbLinear[uint8(pr>>8)], srgbLinear[uint8(pg>>8)], srgbLinear[uint8(pb>>8)]})
			}
		}
		var cosX [cx][]float64
		var cosY [cy][]float64
		for i := range cosX {
			for _, x := range xs {
				cosX[i] = append(cosX[i], math.Cos(math.Pi*float64(i)*float64(x)/float64(w)))
			}
		}
		for j := range cosY {
			for _, y := range ys {
				cosY[j] = append(cosY[j], math.Cos(math.Pi*float64(j)*float64(y)/float64(h)))
			}
		}
		var factors [cy][cx][3]float64
		for j := 0; j < cy; j++ {
			for i := 0; i < cx; i++ {
				norm := 2.0
				if i == 0 && j == 0 {
					norm = 1
				}
				var r, g, bl float64
				k := 0
				for yi := range ys {
					for xi := range xs {
						basis := cosX[i][xi] * cosY[j][yi]
						p := lin[k]
						r += basis * p[0]
						g += basis * p[1]
						bl += basis * p[2]
						k++
					}
				}
				scale := norm / float64(k)
				factors[j][i] = [3]float64{r * scale, g * scale, bl * scale}
			}
		}
		return []byte(encodeBlurHash(factors))
	}))
}

// encodeBlurHash turns 4x3 DCT factors into the blurhash string.
func encodeBlurHash(factors [3][4][3]float64) string {
	const cx, cy = 4, 3
	var sb strings.Builder
	sb.WriteString(base83((cx-1)+(cy-1)*9, 1))
	maxAC := 0.0
	for j := 0; j < cy; j++ {
		for i := 0; i < cx; i++ {
			if i == 0 && j == 0 {
				continue
			}
			for _, v := range factors[j][i] {
				maxAC = math.Max(maxAC, math.Abs(v))
			}
		}
	}
	qMax := int(math.Max(0, math.Min(82, math.Floor(maxAC*166-0.5))))
	acMax := float64(qMax+1) / 166
	sb.WriteString(base83(qMax, 1))
	dc := factors[0][0]
	sb.WriteString(base83(linearToSRGB(dc[0])<<16+linearToSRGB(dc[1])<<8+linearToSRGB(dc[2]), 4))
	for j := 0; j < cy; j++ {
		for i := 0; i < cx; i++ {
			if i == 0 && j == 0 {
				continue
			}
			q := func(v float64) int {
				return int(math.Max(0, math.Min(18, math.Floor(signPow(v/acMax, 0.5)*9+9.5))))
			}
			f := factors[j][i]
			sb.WriteString(base83(q(f[0])*19*19+q(f[1])*19+q(f[2]), 2))
		}
	}
	return sb.String()
}

func hexOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
