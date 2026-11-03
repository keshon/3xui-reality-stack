package xui

import (
	"context"
	"os"
	"path/filepath"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/host"
)

// Preflight is a preview for a fresh installation with default stack ports.
// It neither adopts existing resources nor certifies an existing stack's health.
func (s Stack) Preflight(ctx context.Context, root string) (deployment.PreflightReport, error) {
	report := deployment.PreflightReport{Stack: stackID, Status: "blocked", Installation: "unknown", Checks: []deployment.Check{}, Observation: "read-only local host snapshot for default fresh-install ports; no resources reserved; DNS, certificates, firewall/provider policy, disk capacity and authenticated transfers not verified"}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	report.Installation = installation(root)
	if report.Installation == "managed" {
		if _, _, err := readDraft(root); err == nil {
			report.Installation = "draft"
		}
	}
	rootOK := report.Installation == "absent" || report.Installation == "empty" || report.Installation == "draft"
	rootCheck := deployment.Check{ID: "installation-root", OK: rootOK}
	if !rootOK {
		rootCheck.Reason = "existing or uninspectable installation requires the existing-installation workflow; fresh deployment is blocked"
	}
	report.Checks = append(report.Checks, rootCheck)
	probe := s.preflightHost
	if probe == nil {
		probe = host.Probe
	}
	observed, err := probe(ctx, []deployment.Port{{Protocol: "tcp", Number: 80}, {Protocol: "tcp", Number: 443}, {Protocol: "udp", Number: 443}, {Protocol: "tcp", Number: 2053}})
	if err != nil {
		return report, err
	}
	report.Host = observed.Facts
	report.Checks = append(report.Checks, observed.Checks...)
	ok := rootOK && len(observed.Checks) != 0
	for _, check := range observed.Checks {
		ok = ok && check.OK
	}
	if ok {
		report.Status = "candidate"
	}
	return report, ctx.Err()
}

func installation(root string) string {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "unknown"
	}
	for _, name := range []string{".stack", ".env", "xui/db"} {
		_, err := os.Lstat(filepath.Join(root, name))
		if err == nil {
			if name == ".stack" {
				return "managed"
			}
			return "legacy"
		}
		if !os.IsNotExist(err) {
			return "unknown"
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "unknown"
	}
	if len(entries) == 0 {
		return "empty"
	}
	return "unmanaged"
}
