package deployment

// AccessReport is an explicit private-channel response, never inspection or
// operation metadata. Adapters select the values an operator needs to connect;
// unrelated environment secrets must not be included.
type AccessReport struct {
	Stack        string        `json:"stack"`
	Links        []AccessValue `json:"links"`
	Credentials  []AccessValue `json:"credentials"`
	ConfigFile   string        `json:"config_file"`
	Instructions []string      `json:"instructions"`
}

type AccessValue struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}
