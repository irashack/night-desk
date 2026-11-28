package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

func TestHeadline(t *testing.T) {
	long := strings.Repeat("界", 179)
	for _, tc := range [][2]string{{"   ", ""}, {" news ", "news"}, {long + "é", long + "é"}, {long + "éé", long + "…"}, {long + " next", long + "…"}, {"first " + long, "first…"}} {
		if got := headline(tc[0]); got != tc[1] {
			t.Errorf("headline = %q, want %q", got, tc[1])
		}
	}
}

func decryptNotification(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	if len(raw) < 103 || binary.BigEndian.Uint32(raw[16:20]) != 4096 || raw[20] != 65 {
		t.Fatal("invalid record header")
	}
	receiver, err := ecdh.P256().NewPrivateKey(decoded(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := ecdh.P256().NewPublicKey(raw[21:86])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := receiver.ECDH(sender)
	if err != nil {
		t.Fatal(err)
	}
	derive := func(secret, salt []byte, info string, n int) []byte {
		k, err := hkdf.Key(sha256.New, secret, salt, info, n)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	ikm := derive(shared, decoded(t, authSecret), "WebPush: info\x00"+string(receiver.PublicKey().Bytes())+string(sender.Bytes()), 32)
	block, err := aes.NewCipher(derive(ikm, raw[:16], "Content-Encoding: aes128gcm\x00", 16))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, derive(ikm, raw[:16], "Content-Encoding: nonce\x00", 12), raw[86:], nil)
	if err != nil || len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatal("invalid encrypted record", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(plain[:len(plain)-1], &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestHeadlineDelivery(t *testing.T) {
	for _, tc := range []struct {
		name              string
		enabled, keys     bool
		lead, kind, title string
		encrypted         bool
	}{
		{"headline", true, true, "a", "news", " Title ", true},
		{"disabled", false, true, "a", "news", "Title", false},
		{"keyless", true, false, "a", "news", "Title", false},
		{"missing lead", true, true, "", "news", "Title", false},
		{"opinion", true, true, "a", "opinion", "Title", false},
		{"blank title", true, true, "a", "news", "  ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := service(t)
			d := setup(t, svc.Client())
			d.cfg.Papers[0].PushHeadline = tc.enabled
			d.cfg.Papers[0].Locale = "en-GB"
			d.cfg.Papers[0].Masthead = "Current masthead"
			ctx := context.Background()
			token := d.signIn(t, "one", "seed")
			scope, err := auth.AuthenticateReader(ctx, d.db, d.cfg, token, time.Now(), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			var keys store.PushKeys
			if tc.keys {
				keys = store.PushKeys{P256DH: uaPublic, Auth: authSecret}
			}
			if err := d.db.AddPushSubscription(ctx, scope, svc.URL+"/device", nil, keys); err != nil {
				t.Fatal(err)
			}
			ed := &edition.Edition{Schema: 1, Lead: tc.lead, Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "a", Kind: tc.kind, Title: tc.title, URL: "https://example.com/a"}}}}}
			if err := d.db.Publish(ctx, d.papers["daily"], "2026-11-03", ed, nil, &store.EditionLayout{Masthead: "Published"}); err != nil {
				t.Fatal(err)
			}
			d.at = time.Now()
			d.tick(t)
			svc.mu.Lock()
			defer svc.mu.Unlock()
			if len(svc.requests) != 1 {
				t.Fatalf("request count %d", len(svc.requests))
			}
			header, raw := svc.requests[0].Header, svc.bodies[0]
			if !tc.encrypted {
				if len(raw) != 0 || header.Get("Content-Encoding") != "" || header.Get("Content-Type") != "" {
					t.Fatal("expected empty push")
				}
				return
			}
			if header.Get("Content-Encoding") != "aes128gcm" || header.Get("Content-Type") != "application/octet-stream" || header.Get("TTL") != "43200" || header.Get("Urgency") != "normal" || header.Get("Topic") != "edition" {
				t.Fatal("incorrect delivery headers")
			}
			verify(t, header.Get("Authorization"))
			want := map[string]any{"web_push": float64(8030), "notification": map[string]any{"title": "Published", "body": "Title", "lang": "en-GB", "navigate": "https://paper.example.com/p/daily/", "silent": false, "tag": "nd-edition"}}
			if !reflect.DeepEqual(decryptNotification(t, raw), want) {
				t.Fatal("unexpected notification")
			}
		})
	}
}
