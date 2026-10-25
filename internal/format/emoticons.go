package format

import "strings"

// Emoticons maps typed emoticons to emoji. Only whole words are converted
// (see EmoticonBefore), so "http://" or "a:b" are left alone. Emoticons that
// also start numbered lists ("B)", "8)") are left out on purpose.
var Emoticons = map[string]string{
	":)": "🙂", ":-)": "🙂", "=)": "🙂", "(:": "🙂",
	":D": "😃", ":-D": "😃", "=D": "😃",
	"xD": "😆", "XD": "😆",
	":'D": "😂", ":')": "🥲",
	";)": "😉", ";-)": "😉",
	":(": "🙁", ":-(": "🙁", "=(": "🙁", "):": "🙁",
	":'(": "😢", ":'-(": "😢",
	":P": "😛", ":-P": "😛", ":p": "😛", ":-p": "😛",
	";P": "😜", ";p": "😜",
	":O": "😮", ":-O": "😮", ":o": "😮", ":-o": "😮",
	":*": "😘", ":-*": "😘",
	":|": "😐", ":-|": "😐",
	":/": "😕", ":-/": "😕", ":\\": "😕", ":-\\": "😕",
	":S": "😖", ":s": "😖",
	":$": "😳", "o_O": "😳", "O_o": "😳", "o.O": "😳", "O.o": "😳",
	">:(": "😠", ">:-(": "😠", ":@": "😠",
	">:)": "😈", ">:-)": "😈",
	"O:)": "😇", "O:-)": "😇", "0:)": "😇",
	"D:": "😧",
	"^^": "😊", "^_^": "😊", "^.^": "😊",
	"-_-": "😑", "-.-": "😑",
	"T_T": "😭", ";_;": "😭",
	":3": "😺",
	"<3": "❤️", "</3": "💔",
	"(y)": "👍", "(Y)": "👍", "(n)": "👎", "(N)": "👎",
	"\\o/": "🙌",
	"o/": "👋", "\\o": "👋",
}

// EmoticonBefore reports whether the text before the cursor ends with an
// emoticon from table that stands on its own (at the start or after
// whitespace), and returns where it starts and its emoji. Emoticons inside
// an unclosed `code span` are not converted.
func EmoticonBefore(before string, table map[string]string) (start int, emoji string, ok bool) {
	if len(table) == 0 || strings.Count(before, "`")%2 == 1 {
		return 0, "", false
	}
	start = strings.LastIndexAny(before, " \t\n") + 1
	word := before[start:]
	if word == "" {
		return 0, "", false
	}
	emoji, ok = table[word]
	return start, emoji, ok && emoji != ""
}
