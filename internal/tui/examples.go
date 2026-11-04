package tui

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// WriteInstallationExamples renders the real UI with illustrative state. It
// calls only Plan on the supplied preview backend; no operation is executed.
func WriteInstallationExamples(ctx context.Context, backend Backend, directory string) error {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	m := newModel(ctx, backend, "Example controller · sample data")
	if m.installer() == nil {
		return fmt.Errorf("installation examples require an installation preview backend")
	}
	write := func(name string) error {
		page := *m
		if page.screen == confirmation || page.screen == installationForm {
			page.screen = m.modalBase
		}
		m.setContent(page.content())
		return os.WriteFile(filepath.Join(directory, name+".svg"), []byte(terminalSVG(m.View().Content)), 0644)
	}
	m.startWizard()
	m.wizard.fields[0].SetValue("example.com")
	if err := write("setup"); err != nil {
		return err
	}
	m.clearWizard()
	m.state.report = deployment.Report{Stack: "3x-ui", Managed: true}
	m.installPreflight = deployment.PreflightReport{Status: "blocked", Checks: []deployment.Check{
		{ID: "installation-root", OK: true}, {ID: "platform", OK: true}, {ID: "docker-inventory", OK: true},
		{ID: "existing-containers", Reason: "Existing containers require manual isolation, including stopped containers."},
	}}
	m.navigate(installationChecks)
	if err := write("readiness"); err != nil {
		return err
	}
	p, err := backend.Plan(ctx)
	if err != nil {
		return err
	}
	m.state.plan = p
	m.review = &deployment.Review{ID: "8a5d20c748ba409a845372b3e67c5d22", Kind: "xui-install-v1", Plan: p, CanApply: true, ExpiresAt: time.Date(2100, 1, 1, 12, 15, 0, 0, time.Local)}
	m.navigate(review)
	m.dispatch("apply")
	if err := write("deploy"); err != nil {
		return err
	}
	m.navigate(operation)
	m.state.operation = deployment.OperationStatus{ID: "24dc118ce7724156bd30e3bb68d48afe", Kind: "xui-install-v1", Status: "running", Completed: 4, Total: 7, CurrentStep: "panel"}
	m.busy, m.mutating = true, true
	m.setContent(m.content())
	if err := write("progress"); err != nil {
		return err
	}
	m.busy, m.mutating = false, false
	m.resize(110, 36)
	m.state.report.Baseline = true
	m.state.operation.Status, m.state.operation.Completed, m.state.operation.CurrentStep = "complete", 7, ""
	m.navigate(access)
	m.accessInfo = &deployment.AccessReport{
		Stack:        "3x-ui",
		Links:        []deployment.AccessValue{{Label: "Panel", Value: "https://ops.example.com/panel-example/"}, {Label: "Public website", Value: "https://www.example.com/"}},
		Credentials:  []deployment.AccessValue{{Label: "Admin username", Value: "admin"}, {Label: "Admin password", Value: "sample-password", Secret: true}},
		ConfigFile:   "/opt/new-stack/.env",
		Instructions: []string{"Open the full panel URL, including its generated path.", "In the panel, open Clients to copy a subscription into HAPP."},
	}
	return write("access")
}

// SVG text preserves the terminal's cell layout without a screenshot dependency.
func terminalSVG(frame string) string {
	lines := strings.Split(ansi.Strip(frame), "\n")
	columns := 0
	for _, line := range lines {
		columns = max(columns, ansi.StringWidth(line))
	}
	width, height := float64(columns)*8.4+32, len(lines)*19+32
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%d" viewBox="0 0 %.0f %d" role="img" aria-label="Stack controller terminal example">`+"\n", width, height, width, height)
	fmt.Fprintf(&out, `<rect width="100%%" height="100%%" rx="8" fill="#17212c"/>`+"\n")
	out.WriteString(`<g font-family="monospace" font-size="14" fill="#d9e2eb" style="white-space:pre">` + "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		text := strings.ReplaceAll(html.EscapeString(line), " ", "&#160;")
		fmt.Fprintf(&out, `<text x="16" y="%d">%s</text>`+"\n", 30+i*19, text)
	}
	out.WriteString("</g>\n</svg>\n")
	return out.String()
}
