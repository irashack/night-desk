// Ported from MuckScraper (https://github.com/grregis/MuckScraper),
// news_fetcher/quality_checks.py at 6c8ffaf. Copyright (c) 2026 grregis. MIT License;
// see THIRD_PARTY_NOTICES.md.

package sourcecheck

import (
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Numeric claims
// ---------------------------------------------------------------------------

var scaleWords = map[string]float64{
	"hundred": 1e2, "thousand": 1e3, "k": 1e3,
	"million": 1e6, "m": 1e6, "mn": 1e6,
	"billion": 1e9, "b": 1e9, "bn": 1e9,
	"trillion": 1e12, "t": 1e12,
}

// Word scales may follow a space ("1.7 million"); single-letter suffixes must
// be attached ("1.7M"), or "3 T-shirts" and "5 m tall" would read as trillions.
var numberRE = regexp.MustCompile(`(?i)(\d[\d,]*(?:\.\d+)?)(?:[\s\p{Z}\x{000B}]+(hundred|thousand|million|billion|trillion|bn|mn)|([kmbt]))?\b`)

func extractNumericValues(text string) map[float64]struct{} {
	values := make(map[float64]struct{})
	if text == "" {
		return values
	}
	for _, match := range numberRE.FindAllStringSubmatch(text, -1) {
		raw := match[1]
		scale := match[2]
		if scale == "" {
			scale = match[3]
		}
		value, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			continue
		}
		if scale != "" {
			value *= scaleWords[strings.ToLower(scale)]
		}
		values[value] = struct{}{}
	}
	return values
}

// ExtractNumericValues returns the numeric values mentioned in text with
// scale suffixes and words resolved, sorted ascending. "1.7M", "1.7 million"
// and "1,700,000" all yield 1700000.
func ExtractNumericValues(text string) []float64 {
	values := extractNumericValues(text)
	ordered := make([]float64, 0, len(values))
	for value := range values {
		ordered = append(ordered, value)
	}
	sort.Float64s(ordered)
	return ordered
}

// twoSigFigs rounds to two significant figures, so a headline's "about 4,900"
// still matches a source's 4,873 instead of being reported as invented.
func twoSigFigs(value float64) float64 {
	if value == 0 {
		return 0
	}
	magnitude := math.Floor(math.Log10(math.Abs(value)))
	factor := math.Pow(10, magnitude-1)
	return math.RoundToEven(value/factor) * factor
}

// UnsupportedNumbers returns the values in text that appear in none of the
// source texts. Matching allows the generated value to be a two-significant-
// figure rounding of a source value. Years are skipped: they are rarely the
// invented figure and often appear only in a dateline the sources do not
// repeat.
func UnsupportedNumbers(text string, sources []string) []string {
	claimed := extractNumericValues(text)
	if len(claimed) == 0 {
		return nil
	}

	supported := make(map[float64]struct{})
	for _, source := range sources {
		for value := range extractNumericValues(source) {
			supported[value] = struct{}{}
		}
	}
	supportedRounded := make(map[float64]struct{}, len(supported))
	for value := range supported {
		supportedRounded[twoSigFigs(value)] = struct{}{}
	}

	claimedValues := make([]float64, 0, len(claimed))
	for value := range claimed {
		claimedValues = append(claimedValues, value)
	}
	sort.Float64s(claimedValues)

	var unsupported []string
	for _, value := range claimedValues {
		if value == math.Trunc(value) && value >= 1900 && value <= 2100 {
			continue
		}
		if _, ok := supported[value]; ok {
			continue
		}
		if _, ok := supportedRounded[twoSigFigs(value)]; ok {
			continue
		}
		unsupported = append(unsupported, formatNumericValue(value))
	}
	return unsupported
}

func formatNumericValue(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// ---------------------------------------------------------------------------
// Entity claims
// ---------------------------------------------------------------------------

var entityStopwords = map[string]struct{}{
	"The": {}, "A": {}, "An": {}, "This": {}, "That": {}, "These": {}, "Those": {}, "It": {}, "He": {}, "She": {},
	"They": {}, "We": {}, "I": {}, "But": {}, "And": {}, "Or": {}, "If": {}, "When": {}, "While": {}, "After": {},
	"Before": {}, "However": {}, "Meanwhile": {}, "Now": {}, "Then": {}, "There": {}, "Here": {}, "What": {},
	"Why": {}, "How": {}, "Who": {}, "Which": {}, "In": {}, "On": {}, "At": {}, "To": {}, "For": {}, "With": {},
	"From": {}, "By": {}, "As": {}, "Of": {}, "Monday": {}, "Tuesday": {}, "Wednesday": {}, "Thursday": {},
	"Friday": {}, "Saturday": {}, "Sunday": {}, "January": {}, "February": {}, "March": {}, "April": {},
	"May": {}, "June": {}, "July": {}, "August": {}, "September": {}, "October": {}, "November": {},
	"December": {},
}

var entityRE = regexp.MustCompile(`\b([A-Z][a-z]{2,})\b`)

// UnsupportedEntities returns capitalized words in text that are present in
// none of the source texts. It is a candidate list for review, not a verdict:
// a summary may legitimately paraphrase a country as "Britain" where every
// source wrote "UK".
func UnsupportedEntities(text string, sources []string) []string {
	if text == "" {
		return nil
	}
	parts := make([]string, 0, len(sources))
	for _, source := range sources {
		parts = append(parts, fold(source))
	}
	haystack := strings.Join(parts, " ")
	if strings.TrimSpace(haystack) == "" {
		return nil
	}

	var found []string
	for _, token := range entityRE.FindAllString(text, -1) {
		if _, stop := entityStopwords[token]; stop {
			continue
		}
		if containsString(found, token) {
			continue
		}
		if !strings.Contains(haystack, fold(token)) {
			found = append(found, token)
		}
	}
	return found
}

// ---------------------------------------------------------------------------
// Titles the sources do not give
// ---------------------------------------------------------------------------

const officePattern = `vice[ -]president|president|prime minister|senator|sen\.|governor|gov\.|secretary of state|secretary|attorney general|speaker|mayor|chancellor|representative|rep\.|ambassador|director|chairman|chair|ceo`

var formerTitleRE = regexp.MustCompile(`\b(?i:former)[\s\p{Z}\x{000B}]+(?:(?:U\.?S\.?|US|American)[\s\p{Z}\x{000B}]+)?((?i:` + officePattern + `))[\s\p{Z}\x{000B}]+((?:(?:[A-Z]\.|[A-Z][\p{L}\p{N}_'\x{2019}-]+)[\s\p{Z}\x{000B}]+){0,2}[A-Z][\p{L}\p{N}_'\x{2019}-]+)`)

var possessiveRE = regexp.MustCompile(`['\x{2019}]s?$`)

var officeForms = map[string]string{
	"gov": "gov|governor", "governor": "gov|governor",
	"sen": "sen|senator", "senator": "sen|senator",
	"rep": "rep|representative", "representative": "rep|representative",
}

// UnsupportedFormerTitles returns "former <office> <Name>" phrases in text that
// no source supports. A source supports one when it pairs "former" with the
// same office and the person's surname within a short span, in either order.
func UnsupportedFormerTitles(text string, sources []string) []string {
	if text == "" {
		return nil
	}
	parts := make([]string, 0, len(sources))
	for _, source := range sources {
		parts = append(parts, fold(source))
	}
	haystack := strings.Join(parts, " ")

	var found []string
	for _, match := range formerTitleRE.FindAllStringSubmatch(text, -1) {
		office := strings.TrimRight(fold(match[1]), ".")
		fields := strings.Fields(match[2])
		if len(fields) == 0 {
			continue
		}
		surname := possessiveRE.ReplaceAllString(fold(fields[len(fields)-1]), "")
		if surname == "" {
			continue
		}
		first, _ := utf8.DecodeRuneInString(surname)
		if !unicode.IsLetter(first) {
			continue
		}
		if !formerTitleSupported(office, surname, haystack) {
			phrase := strings.Join(strings.Fields(match[0]), " ")
			if !containsString(found, phrase) {
				found = append(found, phrase)
			}
		}
	}
	return found
}

func formerTitleSupported(office, surname, haystack string) bool {
	forms, ok := officeForms[office]
	if !ok {
		forms = strings.ReplaceAll(regexp.QuoteMeta(office), " ", "[ -]")
	}
	officeRE := `(?:` + forms + `)\.?`
	word := `[^\s\p{Z}\x{000B}]+`
	space := `[\s\p{Z}\x{000B}]+`
	surnameRE := regexp.QuoteMeta(surname)
	near := `\bformer` + space + `(?:` + word + space + `){0,2}` + officeRE + `\b.{0,40}?\b` + surnameRE + `\b` +
		`|\b` + surnameRE + `\b.{0,40}?\bformer` + space + `(?:` + word + space + `){0,2}` + officeRE + `\b`
	return regexp.MustCompile(near).MatchString(haystack)
}
