package weather

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/testhttp"
)

const forecast = `{"daily":{"time":["2026-10-27"],"temperature_2m_max":[78],"temperature_2m_min":[61],"weather_code":[0],"wind_speed_10m_max":[9]}}`

func TestForecastCache(t *testing.T) {
	network := testhttp.New()
	var calls atomic.Int32
	srv := network.Server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		for k, v := range map[string]string{"latitude": "30.4515", "longitude": "-91.1871", "timezone": "America/Chicago", "temperature_unit": "fahrenheit", "wind_speed_unit": "mph", "daily": "temperature_2m_max,temperature_2m_min,weather_code,wind_speed_10m_max"} {
			if q.Get(k) != v {
				t.Errorf("query %s: %q", k, q.Get(k))
			}
		}
		_, _ = w.Write([]byte(forecast))
	}))
	defer srv.Close()
	c := New(srv.URL)
	c.HTTP.Transport = network.Transport()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := c.Forecast(context.Background(), "a", 30.4515, -91.1871, "America/Chicago", "2026-10-27")
			if d == nil || d.High != 78 || d.Low != 61 || d.Description() != "Clear" || d.Wind != 9 {
				t.Error("bad forecast")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent cache miss duplicated fetches", calls.Load())
	}
	if d := c.Forecast(context.Background(), "a", 30.4515, -91.1871, "America/Chicago", "2026-10-26"); d != nil {
		t.Fatal("forecast invented for missing date")
	}
	_ = c.Forecast(context.Background(), "b", 30.4515, -91.1871, "America/Chicago", "2026-10-27")
	if calls.Load() != 2 {
		t.Fatal("paper caches not distinct")
	}
	c.mu.Lock()
	for _, e := range c.cache {
		e.until = time.Now().Add(-time.Second)
	}
	c.mu.Unlock()
	_ = c.Forecast(context.Background(), "a", 30.4515, -91.1871, "America/Chicago", "2026-10-27")
	if calls.Load() != 3 {
		t.Fatal("cache did not expire")
	}
}
func TestFailureAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
	}{{"HTTP failure", forecast, 503}, {"invalid JSON", "oops", 200}, {"missing arrays", `{"daily":{"time":["2026-10-27"]}}`, 200}, {"null temperature", strings.Replace(forecast, "[78]", "[null]", 1), 200}} {
		t.Run(tc.name, func(t *testing.T) {
			network := testhttp.New()
			var calls atomic.Int32
			srv := network.Server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(srv.URL)
			c.HTTP.Transport = network.Transport()
			for i := 0; i < 2; i++ {
				if c.Forecast(context.Background(), "a", 0, 0, "UTC", "2026-10-27") != nil {
					t.Fatal("failure returned invented weather")
				}
			}
			if calls.Load() != 1 {
				t.Fatal("failures not cached")
			}
		})
	}
	network := testhttp.New()
	srv := network.Server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	c := New(srv.URL)
	c.HTTP.Transport = network.Transport()
	if c.HTTP.Timeout != 5*time.Second {
		t.Fatal("missing 5s timeout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if c.Forecast(ctx, "a", 0, 0, "UTC", "2026-10-27") != nil {
		t.Fatal("timeout returned weather")
	}
	if time.Since(start) > time.Second {
		t.Fatal("did not respect caller deadline")
	}
}
