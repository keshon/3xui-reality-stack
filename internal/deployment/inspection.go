package deployment

// Report is public metadata shared by stack adapters. Live runtime checks are
// separate from local desired/applied state; neither implies network health.
type Report struct {
	Stack       string   `json:"stack"`
	Managed     bool     `json:"managed"`
	Baseline    bool     `json:"baseline"`
	Blockers    []string `json:"blockers"`
	Observation string   `json:"observation"`
}

type Inspection struct {
	Report Report
	Plan   Plan
}

type Check struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

type RuntimeReport struct {
	Stack       string  `json:"stack"`
	Status      string  `json:"status"`
	Checks      []Check `json:"checks"`
	Observation string  `json:"observation"`
}
