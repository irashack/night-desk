package store

import (
	"testing"
)

func TestArchiveDaysCarryLeadTitleAndTolerateMissingLead(t *testing.T) {
	s, scope := eventStore(t)
	ctx := t.Context()
	for date, body := range map[string]string{
		"2026-11-01": `{"schema":2,"lead":"b","sections":[{"key":"x","items":[{"id":"a","title":"Not the lead"}]},{"key":"y","items":[{"id":"b","title":"The lead story"}]}]}`,
		"2026-11-02": `{"schema":2,"lead":"gone","sections":[{"key":"x","items":[{"id":"a","title":"Not the lead"}]}]}`,
		"2026-11-03": `{"schema":2,"sections":[]}`,
	} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO editions(paper_id,date,schema,body,published_at) VALUES (?,?,?,?,?)`, "daily", date, 2, body, now()); err != nil {
			t.Fatal(err)
		}
	}
	days, err := s.ArchiveDays(ctx, scope, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []ArchiveDay{{"2026-11-03", ""}, {"2026-11-02", ""}, {"2026-11-01", "The lead story"}}
	if len(days) != 3 || days[0] != want[0] || days[1] != want[1] || days[2] != want[2] {
		t.Fatalf("got %+v", days)
	}
	days, err = s.ArchiveDays(ctx, scope, 1, 1)
	if err != nil || len(days) != 1 || days[0].Date != "2026-11-02" {
		t.Fatalf("offset: %+v %v", days, err)
	}
}
