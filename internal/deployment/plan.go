// Package deployment defines the stack-independent, public planning vocabulary.
// Plans describe effects; credentials and executable payloads belong to adapters.
package deployment

type Action struct {
	ID       string   `json:"id"`
	Resource string   `json:"resource"`
	Effects  []string `json:"effects"`
}

type Plan struct {
	Stack    string   `json:"stack"`
	Status   string   `json:"status"` // unchanged, proposed, or blocked
	Reasons  []string `json:"reasons,omitempty"`
	Actions  []Action `json:"actions"`
	ReadOnly bool     `json:"read_only"`
}
