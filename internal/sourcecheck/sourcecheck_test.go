// Ported from MuckScraper (https://github.com/grregis/MuckScraper),
// news_fetcher/quality_checks.py at 6c8ffaf. Copyright (c) 2026 grregis. MIT License;
// see THIRD_PARTY_NOTICES.md.

package sourcecheck

import (
	"reflect"
	"testing"
)

func TestTruncated(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{"whitespace", "   \n\t", false},
		{"period", "The council voted 5-4.", false},
		{"question", "Did the council vote?", false},
		{"exclamation", "The council voted!", false},
		{"double quote", `She said "enough."`, false},
		{"right double quote", "She said “enough.”", false},
		{"right single quote", "She said ‘enough.’", false},
		{"apostrophe", "The members'", false},
		{"closing paren", "The council voted (5-4)", false},
		{"closing bracket", "The council voted [5-4]", false},
		{"guillemet", "The council voted »", false},
		{"ellipsis", "The council voted…", false},
		{"no punctuation", "The council voted 5-4", true},
		{"dangling word", "He was cut off in the", true},
		{"trailing whitespace", "The council voted.  \n", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Truncated(test.text); got != test.want {
				t.Fatalf("Truncated(%q) = %v, want %v", test.text, got, test.want)
			}
		})
	}
}

func TestHeadlineTruncated(t *testing.T) {
	tests := []struct {
		name     string
		headline string
		want     bool
	}{
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"complete", "Trump says he will veto the bill", false},
		{"dangling function word", "Trump says he will", true},
		{"possessive will", "Voters' will", false},
		{"what it is", "What it is", false},
		{"phrasal on", "War drags on", false},
		{"phrasal by", "Rules to Live By", false},
		{"number", "Dies at 35", false},
		{"dangling article", "The mayor and", true},
		{"dangling highly", "The plan was highly", true},
		{"subject pronoun may", "He may", true},
		{"person named Un", "Kim Jong Un", false},
		{"with period", "Trump says he will.", true},
		{"dangling and", "The plan and", true},
		{"dangling word unicode", "The café and", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := HeadlineTruncated(test.headline); got != test.want {
				t.Fatalf("HeadlineTruncated(%q) = %v, want %v", test.headline, got, test.want)
			}
		})
	}
}

func TestSentenceCount(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"whitespace", " \n\t ", 0},
		{"one", "One sentence.", 1},
		{"three", "One. Two! Three?", 3},
		{"abbreviation U.S.", "The U.S. economy grew. It was good.", 2},
		{"initial", "John F. Kennedy spoke. He was president.", 2},
		{"abbreviation No.", "No. 5 happened. Then it stopped.", 2},
		{"quoted period", `He said "no." Then left.`, 2},
		{"closing quote", `She said "stop." He listened.`, 2},
		{"no ending punctuation", "One sentence", 1},
		{"multiple spaces", "One.   Two.", 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SentenceCount(test.text); got != test.want {
				t.Fatalf("SentenceCount(%q) = %d, want %d", test.text, got, test.want)
			}
		})
	}
}

func TestAcronymCasedTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"empty", "", nil},
		{"none", "NATO and GOP meet", nil},
		{"one", "Nato Chief Warns", []string{"Nato"}},
		{"multiple", "Gop senators and Nasa", []string{"Gop", "Nasa"}},
		{"duplicates", "Nato and Nato", []string{"Nato"}},
		{"person named Un", "Kim Jong Un", nil},
		{"lowercase adjective", "un-American", nil},
		{"not an acronym", "Ice and Us and Ai", nil},
		{"sentence word", "A Nato official", []string{"Nato"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AcronymCasedTokens(test.text); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("AcronymCasedTokens(%q) = %#v, want %#v", test.text, got, test.want)
			}
		})
	}
}

func TestExtractNumericValues(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []float64
	}{
		{"empty", "", []float64{}},
		{"letter scale", "1.7M ladders", []float64{1700000}},
		{"word scale", "1.7 million ladders", []float64{1700000}},
		{"commas", "1,700,000 ladders", []float64{1700000}},
		{"hundred", "three hundred", []float64{}},
		{"plain", "The rate was 3.5% and $1,200.", []float64{3.5, 1200}},
		{"no false scale", "3 T-shirts and 5 m tall", []float64{3, 5}},
		{"suffixes", "2k, 3 bn, 4 mn, 5t", []float64{2000, 4e6, 3e9, 5e12}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ExtractNumericValues(test.text)
			if len(got) == 0 && len(test.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ExtractNumericValues(%q) = %#v, want %#v", test.text, got, test.want)
			}
		})
	}
}

func TestUnsupportedNumbers(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		sources []string
		want    []string
	}{
		{"no values", "No figures here.", []string{"Nothing."}, nil},
		{"supported", "The tower is 1.7M feet.", []string{"The tower is 1.7 million feet."}, nil},
		{"same number different format", "It was 1,700,000.", []string{"It was 1.7M."}, nil},
		{"significant figures", "About 4,900 people came.", []string{"4,873 people came."}, nil},
		{"significant figures reversed", "About 4,873 people came.", []string{"4,900 people came."}, nil},
		{"magnitude invented", "Over 17 million ladders.", []string{"1.7M ladders."}, []string{"17000000"}},
		{"year skipped", "In 2024, 17 people died.", []string{"17 people died."}, nil},
		{"year in source only", "In 2024, 17 people died.", []string{"In 2023, 17 people died."}, nil},
		{"sorted", "50 cats and 30 dogs and 70 birds.", []string{"30 dogs."}, []string{"50", "70"}},
		{"introduced year outside range", "In 1850, 17 people died.", []string{"17 people died."}, []string{"1850"}},
		{"no sources", "Over 17 million ladders.", nil, []string{"17000000"}},
		{"decimal", "It cost 12.75 dollars.", []string{"It cost 10.70 dollars."}, []string{"12.75"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := UnsupportedNumbers(test.text, test.sources)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("UnsupportedNumbers(%q, %#v) = %#v, want %#v", test.text, test.sources, got, test.want)
			}
		})
	}
}

func TestUnsupportedEntities(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		sources []string
		want    []string
	}{
		{"empty text", "", []string{"anything"}, nil},
		{"no sources", "President Zelensky", nil, nil},
		{"blank sources", "President Zelensky", []string{"  "}, nil},
		{"supported", "President Zelensky met Macron.", []string{"President Zelensky met Macron."}, nil},
		{"unsupported", "President Zelensky met Macron.", []string{"Zelensky met the French leader."}, []string{"President", "Macron"}},
		{"stopwords", "The President met Macron on Monday.", []string{"President Macron"}, nil},
		{"accents fold", "Jose met Muller.", []string{"José met Müller."}, nil},
		{"case folds", "Zelensky spoke.", []string{"zelensky spoke."}, nil},
		{"duplicates once", "Macron and Macron.", []string{"Nobody."}, []string{"Macron"}},
		{"short words ignored", "Xi met Li.", []string{"Nobody."}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := UnsupportedEntities(test.text, test.sources)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("UnsupportedEntities(%q, %#v) = %#v, want %#v", test.text, test.sources, got, test.want)
			}
		})
	}
}

func TestUnsupportedFormerTitles(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		sources []string
		want    []string
	}{
		{"empty text", "", []string{"anything"}, nil},
		{"supported forward", "Former President Trump spoke.", []string{"Former President Donald Trump spoke."}, nil},
		{"supported reverse", "Former President Trump spoke.", []string{"Trump, the former president, spoke."}, nil},
		{"unsupported", "Former President Trump spoke.", []string{"Trump spoke."}, []string{"Former President Trump"}},
		{"country prefix", "The former U.S. President Joe Biden spoke.", []string{"Biden spoke."}, []string{"former U.S. President Joe Biden"}},
		{"governor abbreviation", "Former Gov. Jerry Brown spoke.", []string{"Jerry Brown, the former governor, spoke."}, nil},
		{"senator abbreviation", "Former Sen. Bob Dole spoke.", []string{"Former senator Bob Dole spoke."}, nil},
		{"representative abbreviation", "Former Rep. Jane Doe spoke.", []string{"Jane Doe, a former representative, spoke."}, nil},
		{"possessive", "Former President Trump's plan failed.", []string{"Trump plan."}, []string{"Former President Trump's"}},
		{"duplicate once", "Former President Trump and Former President Trump.", []string{"Nobody."}, []string{"Former President Trump"}},
		{"empty sources", "Former President Trump spoke.", nil, []string{"Former President Trump"}},
		{"lowercase phrase", "former president Trump spoke.", []string{"Trump spoke."}, []string{"former president Trump"}},
		{"unsupported office", "Former Mayor Jane Doe spoke.", []string{"Jane Doe spoke."}, []string{"Former Mayor Jane Doe"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := UnsupportedFormerTitles(test.text, test.sources)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("UnsupportedFormerTitles(%q, %#v) = %#v, want %#v", test.text, test.sources, got, test.want)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	t.Run("nil without sources", func(t *testing.T) {
		if got := Check("The council voted", nil); got != nil {
			t.Fatalf("Check with no sources = %#v, want nil", got)
		}
		if got := Check("The council voted", []string{}); got != nil {
			t.Fatalf("Check with empty sources = %#v, want nil", got)
		}
		if got := Check("The council voted", []string{"  \n"}); got != nil {
			t.Fatalf("Check with blank sources = %#v, want nil", got)
		}
	})

	t.Run("clean text", func(t *testing.T) {
		if got := Check("President Zelensky met Macron.", []string{"President Zelensky met Macron."}); got != nil {
			t.Fatalf("Check on clean text = %#v, want nil", got)
		}
	})

	t.Run("rules and severity", func(t *testing.T) {
		got := Check("Former President Trump spoke.", []string{"The President was briefed. A former official said Trump spoke."})
		want := []Finding{{Rule: "unsupported-former-title", Value: "Former President Trump", Severity: "warning"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Check former title = %#v, want %#v", got, want)
		}
	})

	t.Run("number", func(t *testing.T) {
		got := Check("It was 17 million.", []string{"It was 1.7M."})
		want := []Finding{{Rule: "unsupported-number", Value: "17000000", Severity: "warning"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Check number = %#v, want %#v", got, want)
		}
	})

	t.Run("name", func(t *testing.T) {
		got := Check("Zelenskyy spoke.", []string{"Zelensky spoke."})
		want := []Finding{{Rule: "unsupported-name", Value: "Zelenskyy", Severity: "warning"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Check name = %#v, want %#v", got, want)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		got := Check("The council voted", []string{"The council voted."})
		want := []Finding{{Rule: "truncated", Value: "no terminal punctuation", Severity: "warning"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Check truncated = %#v, want %#v", got, want)
		}
	})

	t.Run("deterministic order", func(t *testing.T) {
		text := "Former President Trump met Zelenskyy and 17 million people"
		sources := []string{"Trump met Zelensky and 1.7M people."}
		first := Check(text, sources)
		for i := 0; i < 10; i++ {
			if got := Check(text, sources); !reflect.DeepEqual(got, first) {
				t.Fatalf("Check not deterministic: %#v != %#v", got, first)
			}
		}
		wantRules := []string{"unsupported-number", "unsupported-name", "unsupported-name", "unsupported-name", "unsupported-former-title", "truncated"}
		if len(first) != len(wantRules) {
			t.Fatalf("Check findings = %#v, want %d rules", first, len(wantRules))
		}
		for i, rule := range wantRules {
			if first[i].Rule != rule {
				t.Fatalf("finding %d rule = %q, want %q (%#v)", i, first[i].Rule, rule, first)
			}
			if first[i].Severity != "warning" {
				t.Fatalf("finding %d severity = %q, want warning", i, first[i].Severity)
			}
		}
	})
}

func TestTitlesUseTheHeadlineRule(t *testing.T) {
	sources := []string{"The council passes the budget today."}
	if got := CheckTitle("Council passes budget", sources); got != nil {
		t.Fatalf("a headline without terminal punctuation is not truncated: %v", got)
	}
	if got := CheckTitle("Council votes to", sources); len(got) != 1 || got[0].Rule != ruleTruncated {
		t.Fatalf("a headline ending on a function word is truncated: %v", got)
	}
	if got := Check("The council passes the budget", sources); len(got) != 1 || got[0].Rule != ruleTruncated {
		t.Fatalf("prose without terminal punctuation is truncated: %v", got)
	}
	for _, tc := range []struct {
		text     string
		headline bool
		want     string
	}{
		{"Council passes budget", true, ""},
		{"Council votes to", true, "ends on dangling word 'to'"},
		{"The budget passed.", false, ""},
		{"The budget passed", false, "no terminal punctuation"},
		{"", false, ""},
	} {
		if got := CutOff(tc.text, tc.headline); got != tc.want {
			t.Errorf("CutOff(%q, %v) = %q, want %q", tc.text, tc.headline, got, tc.want)
		}
	}
}
