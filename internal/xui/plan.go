package xui

import (
	"reflect"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// Plan supports only the first migration slice. Other changes are blocked,
// rather than delegated to the broad legacy installer. CLI plans never execute.
func (i Inspection) Plan() deployment.Plan {
	p := deployment.Plan{Stack: stackID, Status: "blocked", ReadOnly: true, Actions: []deployment.Action{}}
	if !i.Report.Managed || !i.Report.Baseline {
		p.Reasons = append([]string{"managed configuration and applied baseline are required"}, i.Report.Blockers...)
		return p
	}
	if len(i.Report.Blockers) != 0 {
		p.Reasons = append([]string(nil), i.Report.Blockers...)
		return p
	}
	if reflect.DeepEqual(i.desired, i.applied) {
		p.Status = "unchanged"
		return p
	}
	if i.desired.Env["ROLE"] == "node" {
		p.Reasons = []string{"traffic-node password changes need readiness-marker integration before Go apply is supported"}
		return p
	}
	oldEnv := make(map[string]string, len(i.applied.Env))
	for k, v := range i.applied.Env {
		oldEnv[k] = v
	}
	password := i.desired.Env["PANEL_PASS"]
	oldEnv["PANEL_PASS"] = password
	onlyPassword := reflect.DeepEqual(oldEnv, i.desired.Env) &&
		reflect.DeepEqual(i.applied.Users, i.desired.Users) &&
		reflect.DeepEqual(i.applied.Nodes, i.desired.Nodes)
	if !onlyPassword || password == "" || controls(password) || controls(i.applied.Env["PANEL_PASS"]) || i.applied.Env["PANEL_PASS"] == "" || i.desired.Env["PANEL_USER"] == "" {
		p.Reasons = []string{"only an isolated panel password change is supported by this experimental planner"}
		return p
	}
	p.Status = "proposed"
	p.Actions = []deployment.Action{
		{ID: "snapshot", Resource: "managed-state", Effects: []string{"capture configuration and consistent panel database recovery point"}},
		{ID: "panel-password", Resource: "panel-administrator", Effects: []string{"change password through the panel API; no service restart"}},
		{ID: "verify", Resource: "panel-administrator", Effects: []string{"authenticate with desired credentials; verify identities and subscription paths are preserved"}},
		{ID: "commit", Resource: "managed-state", Effects: []string{"update credential outputs and applied baseline after verification"}},
	}
	return p
}
