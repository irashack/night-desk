// Ported from MuckScraper (https://github.com/grregis/MuckScraper),
// news_fetcher/copy_detector.py at 6c8ffaf. Copyright (c) 2026 grregis. MIT License;
// see THIRD_PARTY_NOTICES.md.

// Package wire finds articles that reproduce another article's text.
//
// It ports the pure parts of MuckScraper's copy detector: text cleanup,
// word shingles, containment and copy grouping. Database marking is left to
// the caller.
package wire

import (
	"html"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	defaultShingleSize = 8
	defaultMinOverlap  = 0.5
	minTextChars       = 500
	minShingles        = 30
)

var (
	tagRE  = regexp.MustCompile(`<[^>]+>`)
	wordRE = regexp.MustCompile(`[a-z0-9]+`)
)

// Shingles returns the set of size-word shingles in text. Words are runs of
// ASCII letters and digits after lowercasing; punctuation separates them.
// A size that is not positive, or a text shorter than size words, gives an
// empty set.
func Shingles(text string, size int) map[string]struct{} {
	shingles := make(map[string]struct{})
	if size <= 0 {
		return shingles
	}
	words := wordRE.FindAllString(strings.ReplaceAll(strings.ToLower(text), "’", "'"), -1)
	for i := 0; i+size <= len(words); i++ {
		shingles[strings.Join(words[i:i+size], " ")] = struct{}{}
	}
	return shingles
}

// Containment returns the share of the smaller shingle set that is also in
// the other set. Two empty sets have no shared text and return zero.
func Containment(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	shared := 0
	for shingle := range small {
		if _, ok := large[shingle]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(small))
}

// Groups returns groups of at least two indices whose texts are copies of
// each other. It uses MuckScraper's default eight-word shingles and half-text
// overlap. Groups and their members are sorted by index, so equal inputs give
// equal output.
//
// As in MuckScraper's detector, texts shorter than 500 characters or with
// fewer than 30 shingles are not compared. Input is treated as article
// content: HTML tags are removed before shingling.
func Groups(texts []string) [][]int {
	return GroupsWith(texts, defaultShingleSize, defaultMinOverlap)
}

// GroupsWith is Groups with an explicit shingle size and minimum containment.
// A group is connected by copy pairs, so a text that overlaps enough with any
// member joins the whole group. Reports are sorted by index for determinism.
func GroupsWith(texts []string, size int, minOverlap float64) [][]int {
	if size <= 0 || len(texts) < 2 {
		return nil
	}

	grams := make([]map[string]struct{}, len(texts))
	usable := make([]bool, len(texts))
	for i, text := range texts {
		plain := plainText(text)
		if utf8.RuneCountInString(plain) < minTextChars {
			continue
		}
		shingles := Shingles(plain, size)
		if len(shingles) < minShingles {
			continue
		}
		grams[i] = shingles
		usable[i] = true
	}

	parent := make([]int, len(texts))
	for i := range parent {
		parent[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := root(a), root(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	for i := 0; i < len(texts); i++ {
		if !usable[i] {
			continue
		}
		for j := i + 1; j < len(texts); j++ {
			if !usable[j] {
				continue
			}
			if Containment(grams[i], grams[j]) >= minOverlap {
				union(i, j)
			}
		}
	}

	members := make(map[int][]int)
	for i := range texts {
		if usable[i] {
			r := root(i)
			members[r] = append(members[r], i)
		}
	}
	groups := make([][]int, 0, len(members))
	for _, group := range members {
		if len(group) < 2 {
			continue
		}
		sort.Ints(group)
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return nil
	}
	sort.Slice(groups, func(i, j int) bool {
		for k := 0; k < len(groups[i]) && k < len(groups[j]); k++ {
			if groups[i][k] != groups[j][k] {
				return groups[i][k] < groups[j][k]
			}
		}
		return len(groups[i]) < len(groups[j])
	})
	return groups
}

// plainText is MuckScraper's plain_text: remove tags, decode entities,
// collapse whitespace and trim.
func plainText(content string) string {
	text := html.UnescapeString(tagRE.ReplaceAllString(content, " "))
	return strings.Join(strings.Fields(text), " ")
}
