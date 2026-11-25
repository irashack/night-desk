package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/config"
)

// stdin is where `init --interactive` reads answers; tests replace it.
var stdin io.Reader = os.Stdin

// ask fills in a standalone paper's settings by asking for each one not
// already given as a flag. An empty answer keeps the default shown.
func ask(in io.Reader, out io.Writer, o *initOptions, given map[string]bool) error {
	r := bufio.NewReader(in)
	question := func(flag, prompt string, value *string, check func(string) error) error {
		if given[flag] {
			return nil
		}
		for {
			fmt.Fprintf(out, "%s [%s]: ", prompt, *value)
			line, err := r.ReadString('\n')
			if err != nil && (err != io.EOF || line == "") {
				if err == io.EOF {
					return errors.New("init: input ended before every question was answered")
				}
				return err
			}
			answer := strings.TrimSpace(line)
			if answer == "" {
				answer = *value
			}
			if check != nil {
				if problem := check(answer); problem != nil {
					fmt.Fprintln(out, "  "+problem.Error())
					continue
				}
			}
			*value = answer
			return nil
		}
	}
	oneOf := func(options ...string) func(string) error {
		return func(v string) error {
			for _, o := range options {
				if v == o {
					return nil
				}
			}
			return fmt.Errorf("choose one of %s", strings.Join(options, ", "))
		}
	}
	sections := strings.Join(o.sections, ", ")
	steps := []struct {
		flag, prompt string
		value        *string
		check        func(string) error
	}{
		{"masthead", "Masthead", &o.masthead, nil},
		{"timezone", "Timezone (IANA, such as Europe/London)", &o.timezone, func(v string) error {
			if _, err := time.LoadLocation(v); err != nil || v == "" || v == "Local" {
				return errors.New("an IANA timezone, such as America/Chicago or Europe/Berlin")
			}
			return nil
		}},
		{"place", "Place name (optional)", &o.place, nil},
		{"coordinates", "Coordinates for weather and sunrise, LATITUDE,LONGITUDE (optional)", &o.coordinates, func(v string) error {
			if v == "" {
				return nil
			}
			probe := initOptions{coordinates: v}
			_, _, err := probe.paperDetails()
			return err
		}},
		{"locale", "Language and region of news searches (such as en-GB, de-DE)", &o.locale, func(v string) error {
			if !validLocale(v) {
				return errors.New("a language and region, such as en-US or fr-CA")
			}
			return nil
		}},
		{"units", "Units: metric or imperial", &o.units, oneOf("metric", "imperial")},
		{"deadline", "Publish by (HH:MM, local time)", &o.deadline, func(v string) error {
			if _, err := time.Parse("15:04", v); err != nil || len(v) != 5 {
				return errors.New("a time such as 06:30")
			}
			return nil
		}},
		{"sections", "Sections, comma-separated", &sections, func(v string) error {
			if len(splitList(v)) == 0 {
				return errors.New("name at least one section")
			}
			return nil
		}},
		{"auth", "Sign-in: local, oidc, proxy or none", &o.mode, oneOf(config.AuthLocal, config.AuthOIDC, config.AuthProxy, config.AuthNone)},
		{"public-url", "Address readers open", &o.publicURL, nil},
	}
	for _, s := range steps {
		if s.flag == "units" && !given["units"] && o.units == "" {
			o.units = defaultUnits(o.locale)
		}
		if err := question(s.flag, s.prompt, s.value, s.check); err != nil {
			return err
		}
	}
	o.sections = splitList(sections)
	return nil
}

func validLocale(v string) bool {
	if len(v) < 5 || len(v) > 6 {
		return false
	}
	lang, region, ok := strings.Cut(v, "-")
	if !ok || len(region) != 2 || len(lang) < 2 || len(lang) > 3 {
		return false
	}
	for _, r := range lang {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	for _, r := range region {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
