package api_test

import (
	"strings"
	"testing"
	"time"
)

func TestGatherDroppedEndpoint(t *testing.T) {
	a := setup(t, nil)
	for _, tc := range []struct {
		body string
		code int
		want string
	}{
		{`{"dropped":[{"rule":"betting","count":2},{"rule":"blocked_outlet","count":1}]}`, 200, `"accepted":3`},
		{`{"dropped":[]}`, 200, `"accepted":0`},
		{`{"dropped":[{"rule":"weather","count":1}]}`, 422, "dropped[0].rule"},
		{`{"dropped":[{"rule":"betting","count":1},{"rule":"betting","count":1}]}`, 422, "must be unique"},
		{`{"dropped":[{"rule":"betting","count":0}]}`, 422, "dropped[0].count"},
		{`{"dropped":[{"rule":"betting","count":1}],"titles":["x"]}`, 422, "unknown field"},
		{`{}`, 422, "must be an array of rule counts"},
	} {
		w := a.request("POST", "/api/v1/gather/dropped", tc.body, a.key)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", tc.body, w.Code, w.Body)
		}
	}
	if w := a.request("POST", "/api/v1/gather/dropped", `{"dropped":[]}`, ""); w.Code != 401 {
		t.Errorf("unauthenticated: %d", w.Code)
	}
	loc, _ := time.LoadLocation("America/Chicago")
	today := time.Now().In(loc).Format("2006-01-02")
	data, err := a.db.Quality(t.Context(), "daily", today, today, nil)
	if err != nil || data.DroppedByRule["betting"] != 2 || data.DroppedByRule["blocked_outlet"] != 1 {
		t.Fatalf("%v %v", data.DroppedByRule, err)
	}
}
