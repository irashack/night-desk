package gather

import (
	"bytes"
	readability "codeberg.org/readeck/go-readability/v2"
	"net/url"
)

// Readability extracts locally from already fetched bytes; it never opens URLs.
type Readability struct{}

func (Readability) Extract(rawURL string, page []byte) (string, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", err
	}
	article, err := readability.FromReader(bytes.NewReader(page), u)
	if err != nil {
		return "", "", err
	}
	var text bytes.Buffer
	if err = article.RenderText(&text); err != nil {
		return "", "", err
	}
	return article.Title(), text.String(), nil
}
