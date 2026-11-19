package gather

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/irashack/night-desk/internal/config"
)

// Built-in ingestion rules, after MuckScraper (MIT): junk that never belongs in
// a paper is dropped before its page is fetched or a model sees it. The rules
// read only the feed title and URL, and are conservative: a story about the
// betting industry or a cartoonist stays; a betting-picks column goes.
var builtinFilters = []struct {
	name string
	re   *regexp.Regexp
}{
	{"betting", regexp.MustCompile(`(?i)\b(best bets?|betting (picks?|tips|preview|odds|lines?|trends)|(picks?|predictions?),? (and |& )?odds|odds,? (picks?|predictions?|lines?)|parlays?|player props?|prop bets?|against the spread|sportsbook (promo|bonus)|promo codes?)\b`)},
	{"fantasy", regexp.MustCompile(`(?i)\b(waiver wire|start(/| or | and | 'em,? )sit|sit 'em|dfs (picks?|lineups?|plays|values?)|fantasy (football|baseball|basketball|hockey|soccer) (rankings?|sleepers?|starts?|sits?|waiver|pickups?|advice|lineups?|picks?|busts?|projections?))\b`)},
	{"roundup", regexp.MustCompile(`(?i)(^(morning|evening|afternoon|midday|daily|weekend|weekly) (briefing|brief|digest|roundup|newsletter)\b|\bwhat (we're|we are) reading\b|\b(news|link|links) roundup\b|\bin case you missed it\b)`)},
	{"cartoon", regexp.MustCompile(`(?i)(\b(editorial|political) cartoons?\b|^cartoons?:|\bcartoon of the day\b)`)},
}

var cartoonPath = regexp.MustCompile(`(?i)/(editorial-)?cartoons?(/|$)`)

type filter struct {
	disabled map[string]bool
	outlets  []string
	keywords []*regexp.Regexp
}

func newFilter(c *config.Filters) filter {
	f := filter{disabled: map[string]bool{}}
	if c == nil {
		return f
	}
	g := *c
	for _, name := range g.Disabled {
		f.disabled[name] = true
	}
	for _, o := range g.BlockedOutlets {
		f.outlets = append(f.outlets, strings.ToLower(o))
	}
	for _, k := range g.BlockedKeywords {
		f.keywords = append(f.keywords, phrase(k))
	}
	return f
}

// phrase matches k case-insensitively as whole words wherever k begins or
// ends with a letter or digit.
func phrase(k string) *regexp.Regexp {
	pattern := regexp.QuoteMeta(k)
	if r := []rune(k); len(r) > 0 {
		if word(r[0]) {
			pattern = `\b` + pattern
		}
		if word(r[len(r)-1]) {
			pattern += `\b`
		}
	}
	return regexp.MustCompile(`(?i)` + pattern)
}

func word(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// drop names the rule that excludes an item, or returns "" to keep it. link is
// the article URL after any Google News resolution; outlet is the feed's title.
func (f filter) drop(title, link, outlet string) string {
	host, path := "", ""
	if u, err := url.Parse(link); err == nil {
		host, path = strings.ToLower(strings.TrimSuffix(u.Hostname(), ".")), u.Path
	}
	outlet = strings.ToLower(strings.TrimSpace(outlet))
	for _, blocked := range f.outlets {
		if outlet == blocked || host == blocked || strings.HasSuffix(host, "."+blocked) {
			return "blocked_outlet"
		}
	}
	for _, k := range f.keywords {
		if k.MatchString(title) {
			return "blocked_keyword"
		}
	}
	for _, b := range builtinFilters {
		if f.disabled[b.name] {
			continue
		}
		if b.re.MatchString(title) || b.name == "cartoon" && cartoonPath.MatchString(path) {
			return b.name
		}
	}
	return ""
}
