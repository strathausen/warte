package format

import (
	"sort"
	"strings"
	"sync"
)

// Category is an emoji picker category, in Signal's picker order.
type Category int

// Emoji picker categories.
const (
	CategorySmileys Category = iota
	CategoryPeople
	CategoryAnimals
	CategoryFood
	CategoryActivities
	CategoryTravel
	CategoryObjects
	CategorySymbols
	CategoryFlags
)

var categoryNames = [...]string{
	CategorySmileys:    "Smileys & Emotion",
	CategoryPeople:     "People & Body",
	CategoryAnimals:    "Animals & Nature",
	CategoryFood:       "Food & Drink",
	CategoryActivities: "Activities",
	CategoryTravel:     "Travel & Places",
	CategoryObjects:    "Objects",
	CategorySymbols:    "Symbols",
	CategoryFlags:      "Flags",
}

var categoryIcons = [...]string{
	CategorySmileys:    "😀",
	CategoryPeople:     "👋",
	CategoryAnimals:    "🐻",
	CategoryFood:       "🍔",
	CategoryActivities: "⚽",
	CategoryTravel:     "🚗",
	CategoryObjects:    "💡",
	CategorySymbols:    "💕",
	CategoryFlags:      "🏁",
}

// String returns the category's display name, e.g. "Animals & Nature".
func (c Category) String() string {
	if c < 0 || int(c) >= len(categoryNames) {
		return "Unknown"
	}
	return categoryNames[c]
}

// Icon returns a representative emoji for the category tab.
func (c Category) Icon() string {
	if c < 0 || int(c) >= len(categoryIcons) {
		return "❓"
	}
	return categoryIcons[c]
}

// Categories returns all picker categories in display order.
func Categories() []Category {
	out := make([]Category, len(categoryNames))
	for i := range out {
		out[i] = Category(i)
	}
	return out
}

// Emoji describes one emoji.
type Emoji struct {
	Char       string   // the emoji itself, fully qualified (with VS16 where needed)
	Name       string   // CLDR name, e.g. "face with tears of joy"
	Shortcodes []string // without colons, e.g. ["joy"]; the first is canonical
	Category   Category
}

// emojiEntry is one row of the generated emojiTable.
type emojiEntry struct {
	char     string
	name     string
	category Category
	codes    []string
}

// extraShortcodes are Slack-style or common aliases added on top of the
// GitHub set. Keys are aliases, values are existing shortcodes.
var extraShortcodes = map[string]string{
	"thumbs_up":                     "+1",
	"thumbs_down":                   "-1",
	"slightly_smiling":              "slightly_smiling_face",
	"simple_smile":                  "slightly_smiling_face",
	"grinning_face":                 "grinning",
	"laughing_crying":               "joy",
	"lol":                           "joy",
	"red_heart":                     "heart",
	"party":                         "tada",
	"party_popper":                  "tada",
	"celebrate":                     "tada",
	"thinking_face":                 "thinking",
	"face_palm":                     "facepalm",
	"check":                         "white_check_mark",
	"x_mark":                        "x",
	"cross_mark":                    "x",
	"slight_smile":                  "slightly_smiling_face",
	"upside_down":                   "upside_down_face",
	"rolling_on_the_floor_laughing": "rofl",
	"surprised":                     "open_mouth",
	"wow":                           "open_mouth",
	"sad":                           "cry",
	"crying_face":                   "cry",
	"angry_face":                    "angry",
	"poop":                          "hankey",
	"shit":                          "hankey",
	"pile_of_poo":                   "hankey",
	"hundred":                       "100",
	"folded_hands":                  "pray",
	"clapping":                      "clap",
	"flexed_biceps":                 "muscle",
	"dead":                          "skull",
	"sparkle_heart":                 "sparkling_heart",
	"rocket_ship":                   "rocket",
	"waving_hand":                   "wave",
	"pride":                         "rainbow_flag",
}

// popularShortcodes ranks shortcodes for completion; earlier is more popular.
var popularShortcodes = []string{
	"joy", "heart", "+1", "rofl", "sob", "smile", "pray", "fire", "tada",
	"slightly_smiling_face", "blush", "heart_eyes", "kissing_heart", "wink",
	"grin", "laughing", "sweat_smile", "thinking", "eyes", "raised_hands",
	"clap", "ok_hand", "wave", "muscle", "100", "sparkles", "white_check_mark",
	"x", "-1", "cry", "open_mouth", "astonished", "scream", "rage", "angry",
	"unamused", "roll_eyes", "smirk", "grimacing", "sunglasses", "relieved",
	"pensive", "disappointed", "weary", "tired_face", "sleeping", "yum",
	"stuck_out_tongue", "stuck_out_tongue_winking_eye", "upside_down_face",
	"innocent", "hugs", "shushing_face", "hand_over_mouth", "zipper_mouth_face",
	"face_with_head_bandage", "nauseated_face", "vomiting_face", "hot_face",
	"cold_face", "partying_face", "pleading_face", "smiling_face_with_three_hearts",
	"star_struck", "exploding_head", "cowboy_hat_face", "nerd_face",
	"skull", "ghost", "alien", "robot", "hankey", "see_no_evil", "facepalm",
	"shrug", "point_right", "point_left", "point_up", "point_down", "v",
	"crossed_fingers", "handshake", "fist_oncoming", "metal", "call_me_hand",
	"broken_heart", "two_hearts", "sparkling_heart", "purple_heart",
	"blue_heart", "green_heart", "yellow_heart", "orange_heart", "black_heart",
	"white_heart", "star", "star2", "boom", "zap", "rainbow", "sunny",
	"coffee", "beer", "beers", "wine_glass", "champagne", "cake", "birthday",
	"pizza", "hamburger", "gift", "balloon", "confetti_ball", "rocket",
	"trophy", "medal_sports", "soccer", "basketball", "dog", "cat",
	"unicorn", "rose", "sunflower", "cherry_blossom", "christmas_tree",
	"jack_o_lantern", "money_with_wings", "moneybag", "bulb", "memo",
	"calendar", "phone", "computer", "lock", "key", "bell", "warning",
	"no_entry", "question", "exclamation", "heavy_check_mark",
	"arrow_right", "arrow_left", "arrow_up", "arrow_down",
}

type emojiIndex struct {
	all        []Emoji
	byCode     map[string]int // shortcode → index into all
	byChar     map[string]int // normalized char → index into all
	codes      []string       // all shortcodes, sorted
	popularity map[string]int // shortcode → rank (lower is more popular)
	byCategory map[Category][]int
}

var (
	indexOnce sync.Once
	index     *emojiIndex
)

func emojis() *emojiIndex {
	indexOnce.Do(func() {
		ix := &emojiIndex{
			all:        make([]Emoji, 0, len(emojiTable)),
			byCode:     map[string]int{},
			byChar:     map[string]int{},
			popularity: map[string]int{},
			byCategory: map[Category][]int{},
		}
		for _, e := range emojiTable {
			i := len(ix.all)
			ix.all = append(ix.all, Emoji{Char: e.char, Name: e.name, Shortcodes: append([]string(nil), e.codes...), Category: e.category})
			for _, c := range e.codes {
				if _, dup := ix.byCode[c]; !dup {
					ix.byCode[c] = i
				}
			}
			if _, dup := ix.byChar[stripVS(e.char)]; !dup {
				ix.byChar[stripVS(e.char)] = i
			}
			ix.byCategory[e.category] = append(ix.byCategory[e.category], i)
		}
		aliases := make([]string, 0, len(extraShortcodes))
		for a := range extraShortcodes {
			aliases = append(aliases, a)
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			if _, exists := ix.byCode[alias]; exists {
				continue
			}
			if i, ok := ix.byCode[extraShortcodes[alias]]; ok {
				ix.byCode[alias] = i
				ix.all[i].Shortcodes = append(ix.all[i].Shortcodes, alias)
			}
		}
		for c := range ix.byCode {
			ix.codes = append(ix.codes, c)
		}
		sort.Strings(ix.codes)
		for rank, c := range popularShortcodes {
			if _, ok := ix.popularity[c]; !ok {
				ix.popularity[c] = rank
			}
		}
		index = ix
	})
	return index
}

// stripVS removes variation selector 16 so that "❤" and "❤️" compare equal.
func stripVS(s string) string { return strings.ReplaceAll(s, "\uFE0F", "") }

// trimColons accepts ":code:", ":code" and "code".
func trimColons(code string) string {
	code = strings.TrimPrefix(code, ":")
	return strings.ToLower(strings.TrimSuffix(code, ":"))
}

// Shortcode returns the emoji for a shortcode such as "smile", ":+1:" or
// ":tada:". Lookup is case-insensitive.
func Shortcode(code string) (string, bool) {
	ix := emojis()
	i, ok := ix.byCode[trimColons(code)]
	if !ok {
		return "", false
	}
	return ix.all[i].Char, true
}

// LookupShortcode returns the full Emoji for a shortcode.
func LookupShortcode(code string) (Emoji, bool) {
	ix := emojis()
	i, ok := ix.byCode[trimColons(code)]
	if !ok {
		return Emoji{}, false
	}
	return ix.all[i], true
}

// LookupEmoji returns the Emoji for an emoji character, ignoring variation
// selector 16 differences.
func LookupEmoji(char string) (Emoji, bool) {
	ix := emojis()
	i, ok := ix.byChar[stripVS(char)]
	if !ok {
		return Emoji{}, false
	}
	return ix.all[i], true
}

// ShortcodeFor returns the canonical shortcode of an emoji character, or "".
func ShortcodeFor(char string) string {
	if e, ok := LookupEmoji(char); ok && len(e.Shortcodes) > 0 {
		return e.Shortcodes[0]
	}
	return ""
}

// ShortcodeMatch is a shortcode completion candidate.
type ShortcodeMatch struct {
	Code  string // without colons
	Emoji string
}

// SearchShortcodes returns shortcodes matching prefix (with or without a
// leading colon, case-insensitive) for completion. Shortcodes starting with
// prefix come first, then shortcodes where one of the "_"-separated words
// starts with it; within each group popular codes come first (by rank), then
// the rest alphabetically. Several codes for the same emoji are collapsed to
// the best-ranked one. limit <= 0 means no limit. An empty prefix returns the
// most popular codes.
func SearchShortcodes(prefix string, limit int) []ShortcodeMatch {
	ix := emojis()
	prefix = trimColons(prefix)
	type cand struct {
		code  string
		group int
		rank  int
		idx   int
	}
	var cands []cand
	for _, c := range ix.codes {
		group := -1
		switch {
		case strings.HasPrefix(c, prefix):
			group = 0
		case strings.Contains(c, "_"+prefix):
			group = 1
		}
		if group < 0 {
			continue
		}
		rank, ok := ix.popularity[c]
		if !ok {
			rank = len(popularShortcodes) + 1
		}
		if prefix == "" && !ok {
			continue
		}
		cands = append(cands, cand{c, group, rank, ix.byCode[c]})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.group != b.group {
			return a.group < b.group
		}
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if len(a.code) != len(b.code) && strings.HasPrefix(b.code, a.code) {
			return true
		}
		return a.code < b.code
	})
	seen := map[int]bool{}
	var out []ShortcodeMatch
	for _, c := range cands {
		if seen[c.idx] {
			continue
		}
		seen[c.idx] = true
		out = append(out, ShortcodeMatch{Code: c.code, Emoji: ix.all[c.idx].Char})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// ReplaceShortcodes replaces every known ":shortcode:" in text with its
// emoji. A shortcode must not directly follow a letter or digit (so "a:b:c"
// and "12:30:45" stay intact). Unknown codes and text inside `backtick` code
// spans are left as is.
func ReplaceShortcodes(text string) string {
	if !strings.Contains(text, ":") {
		return text
	}
	var b strings.Builder
	inCode := false
	for i := 0; i < len(text); {
		c := text[i]
		if c == '`' {
			inCode = !inCode
			b.WriteByte(c)
			i++
			continue
		}
		if c == ':' && !inCode && (i == 0 || !isAlnumByte(text[i-1])) {
			if j := strings.IndexByte(text[i+1:], ':'); j > 0 {
				code := text[i+1 : i+1+j]
				if isShortcodeChars(code) {
					if e, ok := Shortcode(code); ok {
						b.WriteString(e)
						i += j + 2
						continue
					}
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func isShortcodeChars(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '+' || c == '-') {
			return false
		}
	}
	return s != ""
}

// ShortcodeQuery reports whether the text before the cursor ends in an
// in-progress shortcode (":" followed by at least minLen shortcode
// characters, with the colon at the start or after whitespace or an opening
// bracket) and returns the partial code and the byte offset of its colon.
// Composers use it to trigger completion.
func ShortcodeQuery(beforeCursor string, minLen int) (query string, start int, ok bool) {
	i := strings.LastIndexByte(beforeCursor, ':')
	if i < 0 {
		return "", 0, false
	}
	q := beforeCursor[i+1:]
	if len(q) < minLen || (q != "" && !isShortcodeChars(q)) {
		return "", 0, false
	}
	if i > 0 {
		switch beforeCursor[i-1] {
		case ' ', '\t', '\n', '(', '[', '"', '\'':
		default:
			return "", 0, false
		}
	}
	return q, i, true
}

// DefaultReactions returns Signal's default quick reactions:
// ❤️ 👍 👎 😂 😮 😢.
func DefaultReactions() []string {
	return []string{"❤️", "👍", "👎", "😂", "😮", "😢"}
}

// EmojisInCategory returns the emojis of category c in picker order.
func EmojisInCategory(c Category) []Emoji {
	ix := emojis()
	idx := ix.byCategory[c]
	out := make([]Emoji, len(idx))
	for i, k := range idx {
		out[i] = ix.all[k]
	}
	return out
}

// AllEmojis returns every known emoji in picker order.
func AllEmojis() []Emoji {
	ix := emojis()
	return append([]Emoji(nil), ix.all...)
}

// SearchEmoji finds emojis whose name or shortcodes match every
// whitespace-separated word of query (each word must be a prefix of a word of
// the name or of a shortcode part). Results whose canonical shortcode starts
// with the query rank first, then popular ones, then picker order. limit <= 0
// means no limit.
func SearchEmoji(query string, limit int) []Emoji {
	ix := emojis()
	words := strings.Fields(strings.ToLower(strings.Trim(query, ": ")))
	if len(words) == 0 {
		return nil
	}
	type hit struct {
		i, score int
	}
	var hits []hit
	for i, e := range ix.all {
		hay := strings.FieldsFunc(strings.ToLower(e.Name)+" "+strings.Join(e.Shortcodes, " "), func(r rune) bool {
			return r == ' ' || r == '_' || r == '-' || r == ':' || r == ',' || r == '.'
		})
		all := true
		for _, w := range words {
			found := false
			for _, h := range hay {
				if strings.HasPrefix(h, w) {
					found = true
					break
				}
			}
			if !found {
				all = false
				break
			}
		}
		if !all {
			continue
		}
		score := 2 * (len(popularShortcodes) + 1)
		for _, c := range e.Shortcodes {
			if r, ok := ix.popularity[c]; ok && len(popularShortcodes)+1+r < score {
				score = len(popularShortcodes) + 1 + r
			}
		}
		joined := strings.Join(words, "_")
		for _, c := range e.Shortcodes {
			if strings.HasPrefix(c, joined) {
				score -= len(popularShortcodes) + 1
				break
			}
		}
		hits = append(hits, hit{i, score})
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score < hits[b].score })
	var out []Emoji
	for _, h := range hits {
		out = append(out, ix.all[h.i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// SkinTone is a Fitzpatrick skin tone modifier.
type SkinTone rune

// Skin tone modifiers; SkinToneNone leaves emojis unmodified.
const (
	SkinToneNone        SkinTone = 0
	SkinToneLight       SkinTone = 0x1F3FB
	SkinToneMediumLight SkinTone = 0x1F3FC
	SkinToneMedium      SkinTone = 0x1F3FD
	SkinToneMediumDark  SkinTone = 0x1F3FE
	SkinToneDark        SkinTone = 0x1F3FF
)

// WithSkinTone applies tone to a single-person hand or gesture emoji (one
// base code point, optionally followed by VS16) and returns it unchanged
// otherwise. Signal stores the modified emoji in reactions.
func WithSkinTone(char string, tone SkinTone) string {
	if tone == SkinToneNone {
		return char
	}
	rs := []rune(stripVS(char))
	if len(rs) != 1 || !skinToneBase(rs[0]) {
		return char
	}
	return string(rs[0]) + string(rune(tone))
}

// skinToneBase reports whether r accepts an Emoji_Modifier (a practical
// subset of Unicode's Emoji_Modifier_Base: hands, gestures and people).
func skinToneBase(r rune) bool {
	switch {
	case r == 0x261D, r == 0x26F9, r >= 0x270A && r <= 0x270D,
		r == 0x1F385, r >= 0x1F3C2 && r <= 0x1F3C4, r == 0x1F3C7, r >= 0x1F3CA && r <= 0x1F3CC,
		r >= 0x1F442 && r <= 0x1F443, r >= 0x1F446 && r <= 0x1F450,
		r >= 0x1F466 && r <= 0x1F478, r == 0x1F47C, r >= 0x1F481 && r <= 0x1F483,
		r >= 0x1F485 && r <= 0x1F487, r == 0x1F4AA, r >= 0x1F574 && r <= 0x1F575,
		r == 0x1F57A, r == 0x1F590, r >= 0x1F595 && r <= 0x1F596,
		r >= 0x1F645 && r <= 0x1F647, r >= 0x1F64B && r <= 0x1F64F, r == 0x1F6A3,
		r >= 0x1F6B4 && r <= 0x1F6B6, r == 0x1F6C0, r == 0x1F6CC, r == 0x1F90C,
		r == 0x1F90F, r >= 0x1F918 && r <= 0x1F91F, r == 0x1F926, r >= 0x1F930 && r <= 0x1F939,
		r >= 0x1F93C && r <= 0x1F93E, r == 0x1F977, r >= 0x1F9B5 && r <= 0x1F9B6,
		r >= 0x1F9B8 && r <= 0x1F9B9, r == 0x1F9BB, r >= 0x1F9CD && r <= 0x1F9CF,
		r >= 0x1F9D1 && r <= 0x1F9DD, r >= 0x1FAC3 && r <= 0x1FAC5, r >= 0x1FAF0 && r <= 0x1FAF8:
		return true
	}
	return false
}

func isAlnumByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
