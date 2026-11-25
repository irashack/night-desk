// Package weather supplies optional, hourly cached Open-Meteo daily forecasts.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type Day struct {
	Date            string
	High, Low, Wind float64
	Code            int
}
type entry struct {
	until   time.Time
	days    []Day
	loading chan struct{}
}
type Client struct {
	BaseURL string
	HTTP    *http.Client
	mu      sync.Mutex
	cache   map[string]*entry
}

func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.open-meteo.com/v1/forecast"
	}
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 5 * time.Second}, cache: map[string]*entry{}}
}

// Forecast returns date's forecast, in metric units (°C, km/h) when metric
// is set and imperial (°F, mph) otherwise.
func (c *Client) Forecast(ctx context.Context, paper string, lat, lon float64, zone, date string, metric bool) *Day {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	key := fmt.Sprintf("%s/%g/%g/%s/%v", paper, lat, lon, zone, metric)
	c.mu.Lock()
	e := c.cache[key]
	if e != nil && e.loading != nil {
		ch := e.loading
		c.mu.Unlock()
		select {
		case <-ch:
			return c.Forecast(ctx, paper, lat, lon, zone, date, metric)
		case <-ctx.Done():
			return nil
		}
	}
	if e != nil && time.Now().Before(e.until) {
		c.mu.Unlock()
		return find(e.days, date)
	}
	e = &entry{loading: make(chan struct{})}
	c.cache[key] = e
	c.mu.Unlock()
	days := c.fetch(ctx, lat, lon, zone, metric)
	c.mu.Lock()
	e.days = days
	e.until = time.Now().Add(time.Hour)
	close(e.loading)
	e.loading = nil
	c.mu.Unlock()
	return find(days, date)
}
func find(days []Day, date string) *Day {
	for _, d := range days {
		if d.Date == date {
			return &d
		}
	}
	return nil
}
func (c *Client) fetch(ctx context.Context, lat, lon float64, zone string, metric bool) []Day {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil
	}
	q := u.Query()
	q.Set("latitude", strconv.FormatFloat(lat, 'f', -1, 64))
	q.Set("longitude", strconv.FormatFloat(lon, 'f', -1, 64))
	q.Set("timezone", zone)
	q.Set("daily", "temperature_2m_max,temperature_2m_min,weather_code,wind_speed_10m_max")
	if metric {
		q.Set("temperature_unit", "celsius")
		q.Set("wind_speed_unit", "kmh")
	} else {
		q.Set("temperature_unit", "fahrenheit")
		q.Set("wind_speed_unit", "mph")
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil
	}
	var v struct {
		Daily struct {
			Time []string   `json:"time"`
			High []*float64 `json:"temperature_2m_max"`
			Low  []*float64 `json:"temperature_2m_min"`
			Code []*int     `json:"weather_code"`
			Wind []*float64 `json:"wind_speed_10m_max"`
		} `json:"daily"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&v) != nil {
		return nil
	}
	d := v.Daily
	if len(d.Time) != len(d.High) || len(d.Time) != len(d.Low) || len(d.Time) != len(d.Code) || len(d.Time) != len(d.Wind) {
		return nil
	}
	days := make([]Day, 0, len(d.Time))
	for i := range d.Time {
		if d.High[i] == nil || d.Low[i] == nil || d.Wind[i] == nil || d.Code[i] == nil {
			continue
		}
		days = append(days, Day{d.Time[i], *d.High[i], *d.Low[i], *d.Wind[i], *d.Code[i]})
	}
	return days
}
func (d Day) Description() string {
	switch d.Code {
	case 0:
		return "Clear"
	case 1, 2:
		return "Partly cloudy"
	case 3:
		return "Overcast"
	case 45, 48:
		return "Fog"
	case 51, 53, 55, 56, 57:
		return "Drizzle"
	case 61, 63, 65, 66, 67, 80, 81, 82:
		return "Rain"
	case 71, 73, 75, 77, 85, 86:
		return "Snow"
	case 95, 96, 99:
		return "Thunderstorms"
	}
	return "Forecast"
}
