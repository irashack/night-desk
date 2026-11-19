package lean

// Outlet is the public domain and rating needed by the gathering worker.
type Outlet struct {
	Domain string `json:"domain"`
	Lean   Lean   `json:"lean"`
	Outlet string `json:"outlet,omitempty"`
}

// Outlets returns rated domains in file order. Outlet identifies domains of the
// same publisher so the worker spends at most one search slot on that outlet.
func (r *Ratings) Outlets() []Outlet {
	out := []Outlet{}
	if r == nil {
		return out
	}
	for _, rating := range r.Ratings {
		for _, domain := range rating.Domains {
			out = append(out, Outlet{Domain: domain, Lean: rating.Lean, Outlet: normalizeOutlet(rating.Outlet)})
		}
	}
	return out
}
