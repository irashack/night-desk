// Ported from MuckScraper (https://github.com/grregis/MuckScraper),
// news_fetcher/quality_checks.py at 6c8ffaf. Copyright (c) 2026 grregis. MIT License;
// see THIRD_PARTY_NOTICES.md.

// Package sourcecheck ports MuckScraper's deterministic source checks: text
// truncation, numbers, names, former titles, sentence counts and acronym
// casing. Everything here is a pure function over plain strings.
package sourcecheck

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Finding is one warning found by Check.
type Finding struct {
	Rule     string
	Value    string
	Severity string
}

const (
	severityWarning = "warning"

	ruleUnsupportedNumber      = "unsupported-number"
	ruleUnsupportedName        = "unsupported-name"
	ruleUnsupportedFormerTitle = "unsupported-former-title"
	ruleTruncated              = "truncated"
)

// Check compares generated prose (a summary or why) with the extracted texts of
// its sources. It returns one warning per unsupported number, name or former
// title, and one warning when the text looks truncated. With no usable source
// text it returns nil: an unavailable source is unverified, not a failure.
func Check(text string, sources []string) []Finding {
	return check(text, sources, false)
}

// CheckTitle is Check for a headline, which has no terminal punctuation by
// convention: it is truncated only when it ends on a function word.
func CheckTitle(text string, sources []string) []Finding {
	return check(text, sources, true)
}

// CutOff returns why text looks cut off, or "" when it does not; headline
// selects the headline rule. It needs no source text.
func CutOff(text string, headline bool) string {
	if headline {
		return headlineLooksTruncated(text)
	}
	return looksTruncated(text)
}

func check(text string, sources []string, headline bool) []Finding {
	if !hasSourceText(sources) {
		return nil
	}

	var findings []Finding
	for _, value := range UnsupportedNumbers(text, sources) {
		findings = append(findings, Finding{Rule: ruleUnsupportedNumber, Value: value, Severity: severityWarning})
	}
	for _, value := range UnsupportedEntities(text, sources) {
		findings = append(findings, Finding{Rule: ruleUnsupportedName, Value: value, Severity: severityWarning})
	}
	for _, value := range UnsupportedFormerTitles(text, sources) {
		findings = append(findings, Finding{Rule: ruleUnsupportedFormerTitle, Value: value, Severity: severityWarning})
	}
	if reason := CutOff(text, headline); reason != "" {
		findings = append(findings, Finding{Rule: ruleTruncated, Value: reason, Severity: severityWarning})
	}
	return findings
}

func hasSourceText(sources []string) bool {
	for _, source := range sources {
		if strings.TrimSpace(source) != "" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Text health
// ---------------------------------------------------------------------------

// Terminal punctuation a prose passage may end on. Kept as a rune set because
// MuckScraper treats typographic quotes, brackets, the guillemet and the
// ellipsis as sentence endings too. Beyond the Latin set: the CJK full-width
// stops and closing brackets, Arabic and Urdu question mark and full stop,
// the Devanagari danda, and the low-high German closing quotes.
const terminalPunctuation = ".!?\"”’')]»…。！？．」』）؟۔।॥“‘›«"

var danglingWords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "and": {}, "or": {}, "but": {}, "of": {}, "at": {}, "to": {},
	"with": {}, "from": {}, "that": {}, "than": {}, "into": {}, "about": {}, "against": {}, "between": {},
	"while": {}, "because": {}, "be": {}, "been": {}, "has": {}, "have": {}, "had": {}, "would": {},
	"could": {}, "should": {}, "might": {}, "its": {}, "their": {}, "his": {}, "her": {}, "highly": {}, "very": {},
}

var pronounDanglers = map[string]struct{}{"will": {}, "may": {}}

var subjectPronouns = map[string]struct{}{"he": {}, "she": {}, "it": {}, "they": {}, "we": {}, "who": {}, "that": {}, "you": {}, "i": {}}

var (
	lastASCIIWordRE = regexp.MustCompile(`[A-Za-z']+`)
	headlineWordRE  = regexp.MustCompile(`[\p{L}\p{N}_'-]+`)
)

// looksTruncated ports looks_truncated: the reason a prose passage appears cut
// off, or "" when it does not. Prose only; headlines have no terminal
// punctuation by convention and use headlineLooksTruncated.
func looksTruncated(text string) string {
	if text == "" {
		return ""
	}
	stripped := strings.TrimSpace(text)
	if stripped == "" {
		return ""
	}
	last, _ := utf8.DecodeLastRuneInString(stripped)
	if strings.ContainsRune(terminalPunctuation, last) {
		return ""
	}
	words := lastASCIIWordRE.FindAllString(stripped, -1)
	if len(words) > 0 {
		word := words[len(words)-1]
		if _, ok := danglingWords[strings.ToLower(word)]; ok {
			return "ends on dangling word '" + word + "' with no terminal punctuation"
		}
	}
	return "no terminal punctuation"
}

// Truncated reports whether a prose passage looks cut off.
func Truncated(text string) bool {
	return looksTruncated(text) != ""
}

// headlineLooksTruncated ports headline_looks_truncated: a headline has no
// terminal punctuation, so the reliable signal is ending on a function word.
func headlineLooksTruncated(headline string) string {
	if headline == "" {
		return ""
	}
	words := headlineWordRE.FindAllString(strings.TrimSpace(headline), -1)
	if len(words) == 0 {
		return ""
	}
	last := strings.ToLower(words[len(words)-1])
	if _, ok := danglingWords[last]; ok {
		return "ends on dangling word '" + words[len(words)-1] + "'"
	}
	if len(words) > 1 {
		previous := strings.ToLower(words[len(words)-2])
		_, pronoun := subjectPronouns[previous]
		_, dangler := pronounDanglers[last]
		if pronoun && dangler {
			return "ends on dangling '" + words[len(words)-2] + " " + words[len(words)-1] + "'"
		}
	}
	return ""
}

// HeadlineTruncated reports whether a headline ends on a function word.
func HeadlineTruncated(headline string) bool {
	return headlineLooksTruncated(headline) != ""
}

// ---------------------------------------------------------------------------
// Sentences
// ---------------------------------------------------------------------------

var sentenceBreakRE = regexp.MustCompile(`[.!?]['"\x{201d}\x{2019})]*[\s\p{Z}\x{000B}]+`)

// Words whose trailing period is an abbreviation, not a sentence end.
var abbreviations = map[string]struct{}{
	"u.s": {}, "u.k": {}, "u.n": {}, "e.u": {}, "d.c": {}, "gov": {}, "sen": {}, "rep": {}, "rev": {}, "dr": {}, "mr": {},
	"mrs": {}, "ms": {}, "st": {}, "jr": {}, "sr": {}, "gen": {}, "lt": {}, "col": {}, "sgt": {}, "capt": {}, "adm": {},
	"no": {}, "vs": {}, "inc": {}, "corp": {}, "co": {}, "ltd": {}, "a.m": {}, "p.m": {}, "jan": {}, "feb": {}, "aug": {},
	"sept": {}, "sep": {}, "oct": {}, "nov": {}, "dec": {}, "mt": {}, "ft": {}, "approx": {}, "dept": {}, "est": {},
}

// SentenceCount is a rough sentence count for prompts that mandate a range.
// A period after a known abbreviation or a single initial does not end a
// sentence.
func SentenceCount(text string) int {
	if text == "" || strings.TrimSpace(text) == "" {
		return 0
	}
	text = strings.TrimSpace(text)
	count := 1
	for _, match := range sentenceBreakRE.FindAllStringIndex(text, -1) {
		if match[1] >= len(text) {
			break
		}
		if text[match[0]] == '.' {
			matched := text[match[0]:match[1]]
			if strings.TrimSpace(matched[1:]) == "" {
				before := strings.Fields(text[:match[0]])
				word := ""
				if len(before) > 0 {
					word = strings.ToLower(strings.TrimLeft(before[len(before)-1], "(\"'“"))
				}
				if _, ok := abbreviations[word]; ok || (isSingleLetter(word)) {
					continue
				}
			}
		}
		count++
	}
	return count
}

// sentenceCount keeps the lower-case name of the Python source for callers in
// this package; SentenceCount is the public spelling.
func sentenceCount(text string) int {
	return SentenceCount(text)
}

func isSingleLetter(word string) bool {
	runes := []rune(word)
	return len(runes) == 1 && unicode.IsLetter(runes[0])
}

// ---------------------------------------------------------------------------
// Acronym casing
// ---------------------------------------------------------------------------

// Curated acronyms whose Title-case spelling ("Nato", "Gop") is a typo. Every
// entry must be a token that is never a lowercase English word.
var acronyms = map[string]struct{}{
	"NATO": {}, "FBI": {}, "CIA": {}, "NSA": {}, "DOJ": {}, "DHS": {}, "FEMA": {}, "NASA": {}, "CDC": {}, "FDA": {},
	"IRS": {}, "EPA": {}, "GOP": {}, "NFL": {}, "NBA": {}, "WNBA": {}, "MLB": {}, "NHL": {}, "NCAA": {},
	"UEFA": {}, "FIFA": {}, "EU": {}, "UN": {}, "UK": {}, "USA": {}, "NHS": {}, "IMF": {}, "OPEC": {},
	"CEO": {}, "CFO": {}, "COO": {}, "CTO": {}, "SUV": {}, "GDP": {}, "GPS": {}, "NYPD": {}, "LAPD": {},
	"TSA": {}, "ICC": {}, "BBC": {}, "CNN": {}, "NPR": {}, "PBS": {}, "ATF": {}, "DEA": {}, "USDA": {},
}

var acronymWordRE = regexp.MustCompile(`\b[A-Za-z][a-z]+\b`)

// AcronymCasedTokens returns known acronyms written in Title case ("Nato",
// "Gop"), in order and without duplicates.
func AcronymCasedTokens(text string) []string {
	if text == "" {
		return nil
	}
	var found []string
	previous := ""
	for _, token := range acronymWordRE.FindAllString(text, -1) {
		isTitle := token[0] >= 'A' && token[0] <= 'Z' && strings.ToLower(token[1:]) == token[1:]
		isName := strings.EqualFold(token, "un") && strings.EqualFold(previous, "jong")
		_, known := acronyms[strings.ToUpper(token)]
		if isTitle && known && !isName && !containsString(found, token) {
			found = append(found, token)
		}
		previous = token
	}
	return found
}

// acronymCasedTokens keeps the lower-case name of the Python source for
// callers in this package; AcronymCasedTokens is the public spelling.
func acronymCasedTokens(text string) []string {
	return AcronymCasedTokens(text)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Folding
// ---------------------------------------------------------------------------

// foldRuneGroups folds Latin letters (and their compatibility forms) the way
// MuckScraper's _fold does: NFKD, drop combining marks, then casefold. Go's
// standard library has no Unicode normalizer, so the decomposed Latin letters
// are listed explicitly; other scripts fall back to simple lowercasing.
var foldRuneGroups = map[string]string{
	"a":   "\u00aa\u00c0\u00c1\u00c2\u00c3\u00c4\u00c5\u00e0\u00e1\u00e2\u00e3\u00e4\u00e5\u0100\u0101\u0102\u0103\u0104\u0105\u01cd\u01ce\u01de\u01df\u01e0\u01e1\u01fa\u01fb\u0200\u0201\u0202\u0203\u0226\u0227\u1e00\u1e01\u1ea0\u1ea1\u1ea2\u1ea3\u1ea4\u1ea5\u1ea6\u1ea7\u1ea8\u1ea9\u1eaa\u1eab\u1eac\u1ead\u1eae\u1eaf\u1eb0\u1eb1\u1eb2\u1eb3\u1eb4\u1eb5\u1eb6\u1eb7\u212b",
	"b":   "\u1e02\u1e03\u1e04\u1e05\u1e06\u1e07",
	"c":   "\u00c7\u00e7\u0106\u0107\u0108\u0109\u010a\u010b\u010c\u010d\u1e08\u1e09",
	"d":   "\u010e\u010f\u1e0a\u1e0b\u1e0c\u1e0d\u1e0e\u1e0f\u1e10\u1e11\u1e12\u1e13",
	"dz":  "\u01c4\u01c5\u01c6\u01f1\u01f2\u01f3",
	"e":   "\u00c8\u00c9\u00ca\u00cb\u00e8\u00e9\u00ea\u00eb\u0112\u0113\u0114\u0115\u0116\u0117\u0118\u0119\u011a\u011b\u0204\u0205\u0206\u0207\u0228\u0229\u1e14\u1e15\u1e16\u1e17\u1e18\u1e19\u1e1a\u1e1b\u1e1c\u1e1d\u1eb8\u1eb9\u1eba\u1ebb\u1ebc\u1ebd\u1ebe\u1ebf\u1ec0\u1ec1\u1ec2\u1ec3\u1ec4\u1ec5\u1ec6\u1ec7",
	"f":   "\u1e1e\u1e1f",
	"ff":  "\ufb00",
	"ffi": "\ufb03",
	"ffl": "\ufb04",
	"fi":  "\ufb01",
	"fl":  "\ufb02",
	"g":   "\u011c\u011d\u011e\u011f\u0120\u0121\u0122\u0123\u01e6\u01e7\u01f4\u01f5\u1e20\u1e21",
	"h":   "\u0124\u0125\u021e\u021f\u1e22\u1e23\u1e24\u1e25\u1e26\u1e27\u1e28\u1e29\u1e2a\u1e2b\u1e96",
	"i":   "\u00cc\u00cd\u00ce\u00cf\u00ec\u00ed\u00ee\u00ef\u0128\u0129\u012a\u012b\u012c\u012d\u012e\u012f\u0130\u01cf\u01d0\u0208\u0209\u020a\u020b\u1e2c\u1e2d\u1e2e\u1e2f\u1ec8\u1ec9\u1eca\u1ecb",
	"ij":  "\u0132\u0133",
	"j":   "\u0134\u0135\u01f0",
	"k":   "\u0136\u0137\u01e8\u01e9\u1e30\u1e31\u1e32\u1e33\u1e34\u1e35",
	"l":   "\u0139\u013a\u013b\u013c\u013d\u013e\u1e36\u1e37\u1e38\u1e39\u1e3a\u1e3b\u1e3c\u1e3d",
	"lj":  "\u01c7\u01c8\u01c9",
	"m":   "\u1e3e\u1e3f\u1e40\u1e41\u1e42\u1e43",
	"n":   "\u00d1\u00f1\u0143\u0144\u0145\u0146\u0147\u0148\u01f8\u01f9\u1e44\u1e45\u1e46\u1e47\u1e48\u1e49\u1e4a\u1e4b",
	"nj":  "\u01ca\u01cb\u01cc",
	"o":   "\u00ba\u00d2\u00d3\u00d4\u00d5\u00d6\u00f2\u00f3\u00f4\u00f5\u00f6\u014c\u014d\u014e\u014f\u0150\u0151\u01a0\u01a1\u01d1\u01d2\u01ea\u01eb\u01ec\u01ed\u020c\u020d\u020e\u020f\u022a\u022b\u022c\u022d\u022e\u022f\u0230\u0231\u1e4c\u1e4d\u1e4e\u1e4f\u1e50\u1e51\u1e52\u1e53\u1ecc\u1ecd\u1ece\u1ecf\u1ed0\u1ed1\u1ed2\u1ed3\u1ed4\u1ed5\u1ed6\u1ed7\u1ed8\u1ed9\u1eda\u1edb\u1edc\u1edd\u1ede\u1edf\u1ee0\u1ee1\u1ee2\u1ee3",
	"p":   "\u1e54\u1e55\u1e56\u1e57",
	"r":   "\u0154\u0155\u0156\u0157\u0158\u0159\u0210\u0211\u0212\u0213\u1e58\u1e59\u1e5a\u1e5b\u1e5c\u1e5d\u1e5e\u1e5f",
	"s":   "\u015a\u015b\u015c\u015d\u015e\u015f\u0160\u0161\u017f\u0218\u0219\u1e60\u1e61\u1e62\u1e63\u1e64\u1e65\u1e66\u1e67\u1e68\u1e69\u1e9b",
	"ss":  "\u00df\u1e9e",
	"st":  "\ufb05\ufb06",
	"t":   "\u0162\u0163\u0164\u0165\u021a\u021b\u1e6a\u1e6b\u1e6c\u1e6d\u1e6e\u1e6f\u1e70\u1e71\u1e97",
	"u":   "\u00d9\u00da\u00db\u00dc\u00f9\u00fa\u00fb\u00fc\u0168\u0169\u016a\u016b\u016c\u016d\u016e\u016f\u0170\u0171\u0172\u0173\u01af\u01b0\u01d3\u01d4\u01d5\u01d6\u01d7\u01d8\u01d9\u01da\u01db\u01dc\u0214\u0215\u0216\u0217\u1e72\u1e73\u1e74\u1e75\u1e76\u1e77\u1e78\u1e79\u1e7a\u1e7b\u1ee4\u1ee5\u1ee6\u1ee7\u1ee8\u1ee9\u1eea\u1eeb\u1eec\u1eed\u1eee\u1eef\u1ef0\u1ef1",
	"v":   "\u1e7c\u1e7d\u1e7e\u1e7f",
	"w":   "\u0174\u0175\u1e80\u1e81\u1e82\u1e83\u1e84\u1e85\u1e86\u1e87\u1e88\u1e89\u1e98",
	"x":   "\u1e8a\u1e8b\u1e8c\u1e8d",
	"y":   "\u00dd\u00fd\u00ff\u0176\u0177\u0178\u0232\u0233\u1e8e\u1e8f\u1e99\u1ef2\u1ef3\u1ef4\u1ef5\u1ef6\u1ef7\u1ef8\u1ef9",
	"z":   "\u0179\u017a\u017b\u017c\u017d\u017e\u1e90\u1e91\u1e92\u1e93\u1e94\u1e95",
}

var foldRunes = buildFoldRunes()

func buildFoldRunes() map[rune]string {
	folded := make(map[rune]string)
	for replacement, runes := range foldRuneGroups {
		for _, r := range runes {
			folded[r] = replacement
		}
	}
	return folded
}

func fold(text string) string {
	var b strings.Builder
	for _, r := range text {
		if replacement, ok := foldRunes[r]; ok {
			b.WriteString(replacement)
			continue
		}
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
