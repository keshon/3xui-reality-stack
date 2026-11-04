package deployment

type Port struct {
	Protocol string `json:"protocol"`
	Number   int    `json:"number"`
}

type PortObservation struct {
	Port
	Known    bool `json:"known"`
	Occupied bool `json:"occupied"`
}

type HostFacts struct {
	OS              string            `json:"os"`
	Architecture    string            `json:"architecture"`
	Distribution    string            `json:"distribution,omitempty"`
	Containers      int               `json:"containers"`
	ComposeProjects int               `json:"compose_projects"`
	CustomNetworks  int               `json:"custom_networks"`
	Volumes         int               `json:"volumes"`
	Ports           []PortObservation `json:"ports"`
}

// Candidate is an observed absence of known conflicts, never permission or
// proof of readiness to deploy. Preflight does not reserve host resources.
type PreflightReport struct {
	Stack        string    `json:"stack"`
	Status       string    `json:"status"`
	Installation string    `json:"installation"`
	Host         HostFacts `json:"host"`
	Checks       []Check   `json:"checks"`
	Observation  string    `json:"observation"`
}
