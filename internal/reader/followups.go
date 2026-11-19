package reader

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
)

// Follow-ups are bounded: one query reads at most this many later snapshots,
// and each item shows at most maxFollowUps of them.
const (
	followUpPool = 500
	maxFollowUps = 3
)

type followUp struct {
	Title, URL, Outlet string
	Gathered, Date     string
}

// followUps finds later coverage of an archived edition's news in the paper's
// candidate pool: snapshots gathered after the edition's date, within
// retention, that share a candidate group with an item's own sources or pass
// sameStory. Today's edition and papers without a gathering worker get none.
func (s *Server) followUps(ctx context.Context, scope auth.PaperScope, paper config.Paper, date string, ed *edition.Edition) (map[string][]followUp, error) {
	loc, err := time.LoadLocation(paper.Timezone)
	if err != nil {
		return nil, err
	}
	now := s.now().In(loc)
	if date >= now.Format("2006-01-02") {
		return nil, nil
	}
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, err
	}
	since := day.AddDate(0, 0, 1)
	retention := paper.RetentionDays
	if retention == 0 {
		retention = 14
	}
	if floor := now.AddDate(0, 0, -retention); since.Before(floor) {
		since = floor
	}
	type story struct {
		item         edition.Item
		own          map[string]bool
		title, words map[string]bool
	}
	var stories []story
	var links []string
	for _, sec := range ed.Sections {
		for _, it := range sec.Items {
			if it.Kind != "news" && it.Kind != "brief" {
				continue
			}
			own := map[string]bool{it.URL: true}
			links = append(links, it.URL)
			for _, source := range it.Sources {
				own[source.URL] = true
				links = append(links, source.URL)
			}
			stories = append(stories, story{it, own, significant(it.Title), significant(it.Title + " " + it.Summary)})
		}
	}
	if len(stories) == 0 {
		return nil, nil
	}
	pool, err := s.Store.LaterCandidates(ctx, scope, since, links, followUpPool)
	if err != nil || len(pool) == 0 {
		return nil, err
	}
	titles := make([]map[string]bool, len(pool))
	for i, c := range pool {
		titles[i] = significant(c.Title)
	}
	out := map[string][]followUp{}
	for _, st := range stories {
		for i, c := range pool {
			if len(out[st.item.ID]) == maxFollowUps {
				break
			}
			if st.own[c.CanonicalURL] || anyIn(c.Aliases, st.own) {
				continue
			}
			if anyIn(c.Grouped, st.own) || sameStory(st.title, st.words, titles[i]) {
				local := c.FetchedAt.In(loc)
				out[st.item.ID] = append(out[st.item.ID], followUp{Title: c.Title, URL: c.CanonicalURL, Outlet: c.Outlet, Gathered: local.Format("Jan 2"), Date: local.Format("2006-01-02")})
			}
		}
	}
	return out, nil
}

func anyIn(links []string, set map[string]bool) bool {
	for _, link := range links {
		if set[link] {
			return true
		}
	}
	return false
}

// sameStory is the deterministic word-overlap rule. A candidate title matches
// an item when it shares at least three significant words with the item's
// title and summary together, and the Jaccard similarity of the two titles'
// significant words is at least 0.4. Both conditions keep a story on the same
// topic, such as another city's budget vote, from passing as a follow-up.
func sameStory(itemTitle, itemWords, candidate map[string]bool) bool {
	shared := 0
	for w := range candidate {
		if itemWords[w] {
			shared++
		}
	}
	if shared < 3 {
		return false
	}
	common := 0
	for w := range candidate {
		if itemTitle[w] {
			common++
		}
	}
	union := len(itemTitle) + len(candidate) - common
	return union > 0 && float64(common)/float64(union) >= 0.4
}

// significant returns the distinct lowercase words of at least four letters
// that are not stopwords. Punctuation, including apostrophes, splits words.
func significant(text string) map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(w) >= 4 && !stopwords[w] {
			words[w] = true
		}
	}
	return words
}

// Stopwords include news filler that would otherwise join unrelated headlines.
var stopwords = func() map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Fields(`about above after again against also amid among another around away back
	 because been before being below between both came come comes could does doing done down during each even
	 every first from further gets have having here into just last latest like live made make makes many more
	 most much must near need news next only other ours over report reports said says should since some still
	 such take takes than that their theirs them then there these they this those though three through today
	 told under until update upon very want week were what when where which while will with within without
	 would year years yesterday your yours`) {
		set[w] = true
	}
	return set
}()
