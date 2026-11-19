package config

// Operator enables the read-only operational dashboard with a separate
// credential. It never grants an agent capability or a reader session.
type Operator struct {
	KeyHashFile string `json:"key_hash_file,omitempty"`
}
