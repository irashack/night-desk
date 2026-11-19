package almanac

import (
	"math"
	"testing"
	"time"
)

func TestPublishedLunarPosition(t *testing.T) {
	// Meeus, Astronomical Algorithms (2nd ed.), example 47.a: 1992-04-12
	// 00:00 TD, longitude 133.162655°, latitude -3.229126°, distance 368409.7 km.
	// Test in TT by undoing lunar's UTC -> TT correction.
	ra, dec, dist, lon := lunar(2448724.5 - 69.184/86400)
	if math.Abs(norm(lon)-133.162655) > .01 || math.Abs(dist-368409.7) > 25 {
		t.Fatalf("lunar position: longitude %.6f distance %.1f", norm(lon), dist)
	}
	lat := math.Asin(sin(dec)*cos(obliquity((2448724.5-2451545)/36525))-cos(dec)*sin(obliquity((2448724.5-2451545)/36525))*sin(ra)) / rad
	if math.Abs(lat-(-3.229126)) > .01 {
		t.Fatalf("lunar latitude: %.6f", lat)
	}
}
func TestBatonRougeEvents(t *testing.T) {
	// Reference algorithms: NOAA Global Monitoring Laboratory Solar Calculator
	// (gml.noaa.gov/grad/solcalc/calcdetails.html), and Meeus ch. 47 for the Moon.
	// These minute-rounded civil-day regression fixtures use Baton Rouge's
	// 30.4515 N, 91.1871 W, America/Chicago and standard sea-level refraction.
	// They are not an independently retrieved USNO event table: network access
	// is disabled in this slice's sandbox. External event-table verification
	// remains necessary; the published lunar-position example above is independent.
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		date, rise, set, mrise, mset, transit, opposite, phase string
		illum                                                  float64
	}{
		{"2026-01-15", "07:02", "17:26", "04:41", "14:35", "09:39", "22:05", "Waning crescent", .088},
		{"2026-06-21", "06:03", "20:10", "13:00", "00:37", "19:06", "06:45", "First quarter", .479},
		{"2026-10-28", "07:16", "18:20", "20:17", "10:09", "02:42", "15:14", "Waning gibbous", .912},
	} {
		t.Run(tc.date, func(t *testing.T) {
			date, _ := time.Parse("2006-01-02", tc.date)
			d := Compute(date, 30.4515, -91.1871, loc)
			check := func(label string, got time.Time, want string) {
				t.Helper()
				expected, err := time.ParseInLocation("2006-01-02 15:04", tc.date+" "+want, loc)
				if err != nil {
					t.Fatal(err)
				}
				delta := got.Sub(expected)
				if delta < -3*time.Minute || delta > 3*time.Minute {
					t.Errorf("%s: %v want %v ±3m", label, got, expected)
				}
			}
			check("sunrise", d.Sunrise, tc.rise)
			check("sunset", d.Sunset, tc.set)
			check("moonrise", d.Moonrise, tc.mrise)
			check("moonset", d.Moonset, tc.mset)
			if len(d.Major) != 2 || len(d.Minor) != 2 {
				t.Fatalf("missing solunar periods: %+v", d)
			}
			centers := []time.Time{d.Major[0].Start.Add(time.Hour), d.Major[1].Start.Add(time.Hour)}
			for _, p := range d.Major {
				if p.End.Sub(p.Start) != 2*time.Hour {
					t.Fatal("major period width")
				}
			}
			for _, p := range d.Minor {
				if p.End.Sub(p.Start) != time.Hour {
					t.Fatal("minor period width")
				}
			}
			if tc.transit < tc.opposite {
				check("transit", centers[0], tc.transit)
				check("opposite", centers[1], tc.opposite)
			} else {
				check("opposite", centers[0], tc.opposite)
				check("transit", centers[1], tc.transit)
			}
			if d.Phase != tc.phase || math.Abs(d.Illumination-tc.illum) > .005 {
				t.Fatal("phase or illumination incorrect")
			}
		})
	}
}
func TestPolarAndDST(t *testing.T) {
	date, _ := time.Parse("2006-01-02", "2026-06-21")
	d := Compute(date, 89, 0, time.UTC)
	if !d.Sunrise.IsZero() || !d.Sunset.IsZero() {
		t.Fatal("invented polar sun events")
	}
	loc, _ := time.LoadLocation("America/Chicago")
	for _, s := range []string{"2026-03-08", "2026-11-01"} {
		date, _ = time.Parse("2006-01-02", s)
		d = Compute(date, 30.4515, -91.1871, loc)
		for _, event := range []time.Time{d.Sunrise, d.Sunset, d.Moonrise, d.Moonset} {
			if !event.IsZero() && event.Format("2006-01-02") != s {
				t.Fatal("event outside local date", event)
			}
		}
	}
	// The April 8, 2024 eclipse is an independently known new-moon date.
	date, _ = time.Parse("2006-01-02", "2024-04-08")
	d = Compute(date, 30.4515, -91.1871, loc)
	if d.Phase != "New moon" || d.Illumination > .002 {
		t.Fatal("eclipse phase wrong")
	}
}
