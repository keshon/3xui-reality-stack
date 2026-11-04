package deployment

// InitializationReport describes a prepared private draft, not a running or
// applied installation. File names and future stages never contain secrets.
type InitializationReport struct {
	Stack       string   `json:"stack"`
	Status      string   `json:"status"`
	Files       []string `json:"files"`
	Stages      []Action `json:"stages"`
	CanApply    bool     `json:"can_apply"`
	Observation string   `json:"observation"`
}
