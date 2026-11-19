package wire

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPlainText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"tags and entities", `<p>Hello&nbsp;<b>there</b> &amp; welcome</p>`, "Hello there & welcome"},
		{"whitespace", " one\n\t two  ", "one two"},
		{"empty", "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := plainText(test.in); got != test.want {
				t.Fatalf("plainText(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestShingles(t *testing.T) {
	tests := []struct {
		name string
		text string
		size int
		want map[string]struct{}
	}{
		{
			name: "windows",
			text: "One two three four",
			size: 3,
			want: map[string]struct{}{"one two three": {}, "two three four": {}},
		},
		{
			name: "case and curly apostrophe",
			text: "Don’t stop now",
			size: 2,
			want: map[string]struct{}{"don t": {}, "t stop": {}, "stop now": {}},
		},
		{
			name: "too short",
			text: "one two",
			size: 3,
			want: map[string]struct{}{},
		},
		{
			name: "repeated windows collapse",
			text: "a b a b a b",
			size: 2,
			want: map[string]struct{}{"a b": {}, "b a": {}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Shingles(test.text, test.size); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Shingles(%q, %d) = %#v, want %#v", test.text, test.size, got, test.want)
			}
		})
	}
	if got := Shingles("one two three", 0); len(got) != 0 {
		t.Fatalf("size zero gave %#v, want no shingles", got)
	}
	if got := Shingles("one two three", -1); len(got) != 0 {
		t.Fatalf("negative size gave %#v, want no shingles", got)
	}
}

func TestContainment(t *testing.T) {
	a := map[string]struct{}{"a": {}, "b": {}, "c": {}}
	b := map[string]struct{}{"a": {}, "b": {}, "d": {}, "e": {}}
	if got := Containment(a, b); got != 2.0/3.0 {
		t.Fatalf("Containment = %v, want %v", got, 2.0/3.0)
	}
	if got := Containment(a, a); got != 1 {
		t.Fatalf("Containment with itself = %v, want 1", got)
	}
	if got := Containment(nil, a); got != 0 {
		t.Fatalf("Containment with nil = %v, want 0", got)
	}
	if got := Containment(map[string]struct{}{}, map[string]struct{}{}); got != 0 {
		t.Fatalf("Containment of empty sets = %v, want 0", got)
	}
	subset := map[string]struct{}{"a": {}}
	if got := Containment(subset, a); got != 1 {
		t.Fatalf("Containment of subset = %v, want 1", got)
	}
}

// articleText builds a long, mostly unique body. The token prefix makes texts
// built with different tokens unrelated, unlike repeated filler text.
func articleText(token string, words int) string {
	var b strings.Builder
	for i := 0; i < words; i++ {
		fmt.Fprintf(&b, "%s%03d item%03d detail%03d paragraph%03d. ", token, i, i, i, i)
	}
	return b.String()
}

func TestGroups(t *testing.T) {
	a := articleText("alpha", 60)
	b := articleText("alpha", 60)
	c := articleText("charlie", 60)

	// Two independent copy groups, deliberately out of index order.
	got := Groups([]string{c, a, b, c})
	want := [][]int{{0, 3}, {1, 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Groups = %v, want %v", got, want)
	}
}

func TestGroupsHTMLContent(t *testing.T) {
	a := "<p>" + articleText("alpha", 60) + "</p>"
	b := "<div><p>" + articleText("alpha", 60) + "</p></div>"
	got := Groups([]string{a, b})
	want := [][]int{{0, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Groups of HTML copies = %v, want %v", got, want)
	}
}

func TestGroupsIgnoresShortTexts(t *testing.T) {
	a := articleText("alpha", 50)
	// Below MuckScraper's 500-character floor even though the texts are equal.
	short := a[:400]
	if got := Groups([]string{short, short}); got != nil {
		t.Fatalf("Groups of short texts = %v, want nil", got)
	}
}

func TestGroupsIgnoresFewShingles(t *testing.T) {
	// Long enough by character count but only two distinct shingles.
	text := strings.Repeat("one two three four five six seven eight ", 20)
	if got := Groups([]string{text, text}); got != nil {
		t.Fatalf("Groups with too few shingles = %v, want nil", got)
	}
}

func TestGroupsWith(t *testing.T) {
	a := articleText("alpha", 60)
	b := articleText("alpha", 60)
	c := articleText("charlie", 60)
	got := GroupsWith([]string{a, c, b}, 4, 0.9)
	want := [][]int{{0, 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupsWith = %v, want %v", got, want)
	}
	if got := GroupsWith([]string{a, b}, 0, 0.5); got != nil {
		t.Fatalf("GroupsWith size zero = %v, want nil", got)
	}
	if got := GroupsWith([]string{a, b}, 10000, 0.5); got != nil {
		t.Fatalf("GroupsWith size over text length = %v, want nil", got)
	}
}
