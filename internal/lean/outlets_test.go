package lean

import "testing"

func TestOutlets(t *testing.T) {
	var absent *Ratings
	if got := absent.Outlets(); got == nil || len(got) != 0 {
		t.Fatal(got)
	}
	r := testRatings()
	r.Ratings[0].Domains = append(r.Ratings[0].Domains, "other.example")
	got := r.Outlets()
	if len(got) != 6 || got[0].Domain != "left.example" || got[0].Lean != Left || got[1].Domain != "other.example" || got[0].Outlet != got[1].Outlet {
		t.Fatal(got)
	}
}
