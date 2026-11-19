// Package delivery drains Night Desk's durable webhook outbox.
package delivery

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/fetch"
	"github.com/irashack/night-desk/internal/store"
	"github.com/irashack/night-desk/internal/webhook"
)

type Loop struct {
	newClient func(fetch.Allow) *http.Client
	Store     *store.Store
	Config    func() *config.Config
	Now       func() time.Time
	Log       *slog.Logger
}

// Backoff begins at one minute and doubles, capped at 24 hours.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 11 {
		return 24 * time.Hour
	}
	delay := time.Minute * time.Duration(1<<uint(attempt-1))
	if delay > 24*time.Hour {
		return 24 * time.Hour
	}
	return delay
}
func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Run immediately resumes pending deliveries, then checks once per second.
func (l *Loop) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if err := l.Tick(ctx); err != nil && ctx.Err() == nil && l.Log != nil {
			l.Log.Error("webhook outbox operation failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Tick delivers a bounded batch with an injectable clock. Attempts are claimed
// durably before I/O; receivers must deduplicate the stable event ID.
func (l *Loop) Tick(ctx context.Context) error {
	c := l.Config()
	newClient := l.newClient
	if newClient == nil {
		newClient = fetch.New
	}
	client := newClient(fetch.Allow(c.Gather.AllowPrivate))
	// Redirects cannot acknowledge an event or forward its signed payload elsewhere.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer client.CloseIdleConnections()
	for i := 0; i < 100; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		d, err := l.Store.ClaimDelivery(ctx, l.now())
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// Reload configuration and rotation secrets for every attempt. A removed
		// target is retired without sending to a destination no longer authorized.
		p, ok := l.Config().Paper(d.Event.Paper)
		var secrets [][]byte
		if ok {
			for _, w := range p.Webhooks {
				if w.URL == d.URL && w.Accepts(d.Event.Type) {
					secrets = w.Secrets()
					break
				}
			}
		}
		sender := webhook.Sender{Client: client, Now: l.Now, Secrets: func(string) [][]byte { return secrets }}
		status, retry, sendErr := sender.Send(ctx, d.URL, d.Event)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = l.Store.FinishDelivery(ctx, d, l.now(), status, sendErr == nil, retry, Backoff(d.Attempts)); err != nil {
			return err
		}
	}
	return nil
}
