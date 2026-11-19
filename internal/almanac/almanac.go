// Package almanac computes civil-day solar and lunar events without network data.
package almanac

import (
	"math"
	"time"
)

const rad = math.Pi / 180

func sin(d float64) float64  { return math.Sin(d * rad) }
func cos(d float64) float64  { return math.Cos(d * rad) }
func norm(d float64) float64 { return d - 360*math.Floor(d/360) }
func jd(t time.Time) float64 {
	return float64(t.Unix())/86400 + float64(t.Nanosecond())/86400e9 + 2440587.5
}
func obliquity(t float64) float64 {
	return 23 + (26+(21.448-t*(46.815+t*(.00059-t*.001813)))/60)/60 + .00256*cos(125.04-1934.136*t)
}
func equatorial(lon, lat, eps float64) (float64, float64) {
	return norm(math.Atan2(sin(lon)*cos(eps)-math.Tan(lat*rad)*sin(eps), cos(lon)) / rad), math.Asin(sin(lat)*cos(eps)+cos(lat)*sin(eps)*sin(lon)) / rad
}

// NOAA solar equations (Julian centuries, apparent longitude and obliquity).
func solar(j float64) (ra, dec, lon float64) {
	t := (j - 2451545) / 36525
	l := norm(280.46646 + t*(36000.76983+t*.0003032))
	m := norm(357.52911 + t*(35999.05029-.0001537*t))
	c := sin(m)*(1.914602-t*(.004817+.000014*t)) + sin(2*m)*(.019993-.000101*t) + sin(3*m)*.000289
	lon = l + c - .00569 - .00478*sin(125.04-1934.136*t)
	ra, dec = equatorial(lon, 0, obliquity(t))
	return
}

type term struct {
	d, m, p, f int
	a, b       float64
}

// Meeus, Astronomical Algorithms, 2nd ed., tables 47.A/B. Terms smaller
// than ~0.001 degree are omitted; event precision is limited by refraction.
var longitudeTerms = []term{
	{0, 0, 1, 0, 6288774, -20905355}, {2, 0, -1, 0, 1274027, -3699111}, {2, 0, 0, 0, 658314, -2955968}, {0, 0, 2, 0, 213618, -569925}, {0, 1, 0, 0, -185116, 48888}, {0, 0, 0, 2, -114332, -3149}, {2, 0, -2, 0, 58793, 246158}, {2, -1, -1, 0, 57066, -152138}, {2, 0, 1, 0, 53322, -170733}, {2, -1, 0, 0, 45758, -204586}, {0, 1, -1, 0, -40923, -129620}, {1, 0, 0, 0, -34720, 108743}, {0, 1, 1, 0, -30383, 104755}, {2, 0, 0, -2, 15327, 10321}, {0, 0, 1, 2, -12528, 0}, {0, 0, 1, -2, 10980, 79661}, {4, 0, -1, 0, 10675, -34782}, {0, 0, 3, 0, 10034, -23210}, {4, 0, -2, 0, 8548, -21636}, {2, 1, -1, 0, -7888, 24208}, {2, 1, 0, 0, -6766, 30824}, {1, 0, -1, 0, -5163, -8379}, {1, 1, 0, 0, 4987, -16675}, {2, -1, 1, 0, 4036, -12831}, {2, 0, 2, 0, 3994, -10445}, {4, 0, 0, 0, 3861, -11650}, {2, 0, -3, 0, 3665, 14403}, {0, 1, -2, 0, -2689, -7003}, {2, 0, -1, 2, -2602, 0}, {2, -1, -2, 0, 2390, 10056}, {1, 0, 1, 0, -2348, 6322}, {2, -2, 0, 0, 2236, -9884}, {0, 1, 2, 0, -2120, 5751}, {0, 2, 0, 0, -2069, 0}, {2, -2, -1, 0, 2048, -4950}, {2, 0, 1, -2, -1773, 4130}, {2, 0, 0, 2, -1595, 0}, {4, -1, -1, 0, 1215, -3958}, {0, 0, 2, 2, -1110, 0}, {3, 0, -1, 0, -892, 3258},
}
var latitudeTerms = []term{
	{0, 0, 0, 1, 5128122, 0}, {0, 0, 1, 1, 280602, 0}, {0, 0, 1, -1, 277693, 0}, {2, 0, 0, -1, 173237, 0}, {2, 0, -1, 1, 55413, 0}, {2, 0, -1, -1, 46271, 0}, {2, 0, 0, 1, 32573, 0}, {0, 0, 2, 1, 17198, 0}, {2, 0, 1, -1, 9266, 0}, {0, 0, 2, -1, 8822, 0}, {2, -1, 0, -1, 8216, 0}, {2, 0, -2, -1, 4324, 0}, {2, 0, 1, 1, 4200, 0}, {2, 1, 0, -1, -3359, 0}, {2, -1, -1, 1, 2463, 0}, {2, -1, 0, 1, 2211, 0}, {2, -1, -1, -1, 2065, 0}, {0, 1, -1, -1, -1870, 0}, {4, 0, -1, -1, 1828, 0}, {0, 1, 0, 1, -1794, 0}, {0, 0, 0, 3, -1749, 0}, {0, 1, -1, 1, -1565, 0}, {1, 0, 0, 1, -1491, 0}, {0, 1, 1, 1, -1475, 0}, {0, 1, 1, -1, -1410, 0}, {0, 1, 0, -1, -1344, 0}, {1, 0, 0, -1, -1335, 0}, {0, 0, 3, 1, 1107, 0}, {4, 0, 0, -1, 1021, 0},
}

func lunar(j float64) (ra, dec, distance, lon float64) {
	t := (j + 69.184/86400 - 2451545) / 36525
	l := norm(218.3164477 + 481267.88123421*t - .0015786*t*t + t*t*t/538841 - t*t*t*t/65194000)
	d := norm(297.8501921 + 445267.1114034*t - .0018819*t*t + t*t*t/545868 - t*t*t*t/113065000)
	m := norm(357.5291092 + 35999.0502909*t - .0001536*t*t + t*t*t/24490000)
	p := norm(134.9633964 + 477198.8675055*t + .0087414*t*t + t*t*t/69699 - t*t*t*t/14712000)
	f := norm(93.272095 + 483202.0175233*t - .0036539*t*t - t*t*t/3526000 + t*t*t*t/863310000)
	e := 1 - .002516*t - .0000074*t*t
	var sl, sr, sb float64
	for _, v := range longitudeTerms {
		a := float64(v.d)*d + float64(v.m)*m + float64(v.p)*p + float64(v.f)*f
		scale := math.Pow(e, math.Abs(float64(v.m)))
		sl += v.a * scale * sin(a)
		sr += v.b * scale * cos(a)
	}
	for _, v := range latitudeTerms {
		a := float64(v.d)*d + float64(v.m)*m + float64(v.p)*p + float64(v.f)*f
		sb += v.a * math.Pow(e, math.Abs(float64(v.m))) * sin(a)
	}
	a1 := 119.75 + 131.849*t
	a2 := 53.09 + 479264.29*t
	a3 := 313.45 + 481266.484*t
	sl += 3958*sin(a1) + 1962*sin(l-f) + 318*sin(a2)
	sb += -2235*sin(l) + 382*sin(a3) + 175*sin(a1-f) + 175*sin(a1+f) + 127*sin(l-p) - 115*sin(l+p)
	lon = l + sl/1e6
	distance = 385000.56 + sr/1000
	ra, dec = equatorial(lon, sb/1e6, obliquity(t))
	return
}
func hourAngle(j, lon, ra float64) float64 {
	t := (j - 2451545) / 36525
	return norm(280.46061837+360.98564736629*(j-2451545)+.000387933*t*t-t*t*t/38710000+lon-ra+180) - 180
}
func altitude(lat, dec, h float64) float64 {
	return math.Asin(sin(lat)*sin(dec)+cos(lat)*cos(dec)*cos(h)) / rad
}

type Period struct{ Start, End time.Time }
type Day struct {
	Sunrise, Sunset, Moonrise, Moonset time.Time
	Phase                              string
	Illumination                       float64
	Major, Minor                       []Period
}

// Compute uses the date's calendar day in loc, including 23/25-hour DST days.
// Zero event times mean that the event does not occur on that civil day.
func Compute(date time.Time, lat, lon float64, loc *time.Location) Day {
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	solarHeight := func(t time.Time) float64 {
		ra, dec, _ := solar(jd(t))
		return altitude(lat, dec, hourAngle(jd(t), lon, ra)) + .8333
	}
	moonHeight := func(t time.Time) float64 {
		ra, dec, dist, _ := lunar(jd(t))
		h0 := .7275*math.Asin(6378.14/dist)/rad - .5667
		return altitude(lat, dec, hourAngle(jd(t), lon, ra)) - h0
	}
	out := Day{}
	for _, v := range crossings(start, end, solarHeight) {
		if v.up {
			out.Sunrise = v.t
		} else {
			out.Sunset = v.t
		}
	}
	for _, v := range crossings(start, end, moonHeight) {
		if v.up {
			out.Moonrise = v.t
		} else {
			out.Moonset = v.t
		}
		out.Minor = append(out.Minor, Period{v.t.Add(-30 * time.Minute), v.t.Add(30 * time.Minute)})
	}
	transit := func(t time.Time) float64 { ra, _, _, _ := lunar(jd(t)); return sin(hourAngle(jd(t), lon, ra)) }
	for _, v := range crossings(start, end, transit) {
		out.Major = append(out.Major, Period{v.t.Add(-time.Hour), v.t.Add(time.Hour)})
	}
	noon := start.Add(end.Sub(start) / 2)
	_, _, _, ml := lunar(jd(noon))
	_, _, sl := solar(jd(noon))
	phase := norm(ml - sl)
	out.Illumination = (1 - cos(phase)) / 2
	names := []string{"New moon", "Waxing crescent", "First quarter", "Waxing gibbous", "Full moon", "Waning gibbous", "Last quarter", "Waning crescent"}
	out.Phase = names[int(math.Floor((phase+22.5)/45))%8]
	return out
}

type crossing struct {
	t  time.Time
	up bool
}

func crossings(start, end time.Time, f func(time.Time) float64) []crossing {
	var out []crossing
	a := start
	fa := f(a)
	for a.Before(end) {
		b := a.Add(10 * time.Minute)
		if b.After(end) {
			b = end
		}
		fb := f(b)
		if (fa < 0 && fb >= 0) || (fa >= 0 && fb < 0) {
			lo, hi := a, b
			for hi.Sub(lo) > time.Second {
				mid := lo.Add(hi.Sub(lo) / 2)
				if (f(mid) >= 0) == (fa >= 0) {
					lo = mid
				} else {
					hi = mid
				}
			}
			t := lo.Add(hi.Sub(lo) / 2)
			if t.Before(end) {
				out = append(out, crossing{t, fb > fa})
			}
		}
		a = b
		fa = fb
	}
	return out
}
