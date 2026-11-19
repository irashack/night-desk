package store

import "testing"

func TestItemIdentityURL(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		same bool
	}{
		{"HTTPS://EXAMPLE.COM/story/?utm_source=feed&keep=1&utm_medium=rss", "https://example.com/story?keep=1", true},
		{"https://example.com/?UTM_Source=feed", "https://example.com", true},
		{"https://example.com/story?b=2&a=1", "https://example.com/story?a=1&b=2", true},
		{"https://example.com/story#heading", "https://example.com/story", true},
		{"https://example.com/story?keep=1", "https://example.com/story?keep=2", false},
		{"https://example.com/story%2F", "https://example.com/story", false},
		{"https://example.com/story?keep=1;other=2", "https://example.com/story", false},
		{"https://example.com/Story", "https://example.com/story", false},
		{"https://example.com/story", "http://example.com/story", false},
	} {
		if same := itemIdentityURL(tt.a) == itemIdentityURL(tt.b); same != tt.same {
			t.Errorf("%s and %s: equal=%v, want %v", tt.a, tt.b, same, tt.same)
		}
	}
}
