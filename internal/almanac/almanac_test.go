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
func TestLisbonEvents(t *testing.T) {
	// Reference algorithms: NOAA Global Monitoring Laboratory Solar Calculator
	// (gml.noaa.gov/grad/solcalc/calcdetails.html), and Meeus ch. 47 for the Moon.
	// Fixtures are for Lisbon, 38.7223 N, 9.1393 W, Europe/Lisbon, with standard
	// sea-level refraction (-0°34' horizon, upper limb). Times and illumination
	// come from an independent implementation, PyEphem 4.2.1 (libastro), rounded
	// to the minute; the code under test agrees with it to within a few seconds.
	// The USNO one-day rise/set/transit table (aa.usno.navy.mil) gives the same
	// minutes for the sun and moon rise, set and upper transit on all three dates.
	loc, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		date, rise, set, mrise, mset, transit, opposite, phase string
		illum                                                  float64
	}{
		{"2026-01-15", "07:53", "17:39", "05:29", "14:26", "09:59", "22:25", "Waning crescent", .104},
		{"2026-06-21", "06:12", "21:05", "13:18", "01:05", "19:28", "07:07", "First quarter", .454},
		{"2026-10-28", "06:59", "17:41", "19:08", "09:51", "02:00", "14:31", "Waning gibbous", .924},
	} {
		t.Run(tc.date, func(t *testing.T) {
			date, _ := time.Parse("2006-01-02", tc.date)
			d := Compute(date, 38.7223, -9.1393, loc)
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
	loc, _ := time.LoadLocation("Europe/Lisbon")
	for _, s := range []string{"2026-03-29", "2026-10-25"} {
		date, _ = time.Parse("2006-01-02", s)
		d = Compute(date, 38.7223, -9.1393, loc)
		for _, event := range []time.Time{d.Sunrise, d.Sunset, d.Moonrise, d.Moonset} {
			if !event.IsZero() && event.Format("2006-01-02") != s {
				t.Fatal("event outside local date", event)
			}
		}
	}
	// The April 8, 2024 eclipse is an independently known new-moon date.
	date, _ = time.Parse("2006-01-02", "2024-04-08")
	d = Compute(date, 38.7223, -9.1393, loc)
	if d.Phase != "New moon" || d.Illumination > .002 {
		t.Fatal("eclipse phase wrong")
	}
}
