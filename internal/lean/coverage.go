package lean

import (
	"fmt"
	"net"
	"strconv"

	"golang.org/x/net/publicsuffix"
)

// Bar is a story's coverage over its distinct outlets. Left, Center and Right
// count rated outlets; Unrated and Opinion are counted but excluded from those
// three. FetchFailed is reported separately and never counted as silence.
// Blindspot is "", "left" or "right"; Attribution is the ratings source's
// required attribution.
type Bar struct {
	Left        int
	Center      int
	Right       int
	Unrated     int
	Opinion     int
	FetchFailed int
	Blindspot   string
	Attribution string
}

// Coverage counts one outlet once over the story's distinct sources. Sources in
// one non-zero WireGroup count once, using the first. Opinion pieces take
// precedence and are counted only as Opinion. A rated outlet contributes to
// Left (left and lean-left), Center, or Right (right and lean-right). Outlets
// with no rating contribute to Unrated.
//
// An outlet's identity comes from its source URL, never from the supplied
// display name: a URL whose host matches a rated domain is identified by that
// rating's outlet, and an unrated URL is identified by its registrable domain.
// This keeps five differently named links to one outlet from counting five
// times and meeting the blindspot threshold.
func Coverage(r *Ratings, sources []Source, fetchFailed int) Bar {
	b := Bar{FetchFailed: fetchFailed}
	if r != nil {
		b.Attribution = r.Source.Attribution
	}
	seenOutlet := make(map[string]bool, len(sources))
	seenWire := make(map[int]bool)
	for i, src := range sources {
		if src.WireGroup != 0 {
			if seenWire[src.WireGroup] {
				continue
			}
			seenWire[src.WireGroup] = true
		}

		host := hostOf(src.URL)
		rating, rated := r.lookup(src.Outlet, src.URL)
		key := ""
		switch {
		case rated && normalizeOutlet(rating.Outlet) != "":
			key = "outlet:" + normalizeOutlet(rating.Outlet)
		case host != "":
			key = "domain:" + registrableDomain(host)
		case normalizeOutlet(src.Outlet) != "":
			key = "name:" + normalizeOutlet(src.Outlet)
		default:
			key = "source:" + strconv.Itoa(i)
		}
		if seenOutlet[key] {
			continue
		}
		seenOutlet[key] = true

		if src.Opinion {
			b.Opinion++
			continue
		}
		if !rated {
			b.Unrated++
			continue
		}
		switch rating.Lean {
		case Left, LeanLeft:
			b.Left++
		case Center:
			b.Center++
		case Right, LeanRight:
			b.Right++
		default:
			b.Unrated++
		}
	}
	b.Blindspot = blindspot(b)
	return b
}

// registrableDomain reduces a normalized host to the registrable domain used as
// an unrated outlet's identity. It is deliberately a small heuristic rather
// than a public-suffix lookup: a host under a common category suffix such as
// co.uk or com.au keeps three labels, and every other host keeps two.
func registrableDomain(host string) string {
	host = normalizeDomain(host)
	if host == "" || net.ParseIP(host) != nil {
		return host
	}
	if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return domain
	}
	return host
}

// blindspot marks a side with at most 10% of rated outlets when the other side
// has at least 50%, and only with at least five rated outlets. Integer
// comparisons make the boundaries exact.
func blindspot(b Bar) string {
	rated := b.Left + b.Center + b.Right
	if rated < 5 {
		return ""
	}
	leftLow, rightLow := b.Left*10 <= rated, b.Right*10 <= rated
	leftHigh, rightHigh := b.Left*2 >= rated, b.Right*2 >= rated
	switch {
	case leftLow && rightHigh:
		return "left"
	case rightLow && leftHigh:
		return "right"
	}
	return ""
}

// Label is the bar's reader-facing summary. Unrated, opinion and failed-fetch
// counts and the attribution are carried by the Bar fields and shown alongside
// it rather than in this sentence.
func (b Bar) Label() string {
	return fmt.Sprintf("Coverage found among monitored sources: %d left · %d center · %d right",
		b.Left, b.Center, b.Right)
}
