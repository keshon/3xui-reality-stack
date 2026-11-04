package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

func (m *model) View() tea.View { return m.render() }

func planContent(p deployment.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan: %s\n", clean(p.Status))
	for _, reason := range p.Reasons {
		fmt.Fprintf(&b, "\n%s\n", clean(reason))
	}
	for i, action := range p.Actions {
		fmt.Fprintf(&b, "\n%d. %s\n", i+1, clean(action.Resource))
		for _, effect := range action.Effects {
			fmt.Fprintf(&b, "   %s\n", clean(effect))
		}
	}
	if p.Status == "unchanged" {
		b.WriteString("\nNo pending changes.\n")
	}
	return b.String()
}

func (m *model) content() string {
	var b strings.Builder
	switch m.screen {
	case access:
		return m.accessContent()
	case installationChecks:
		return m.installationContent()
	case home:
		labels := m.navigationLabels()
		for i, label := range labels {
			if i == m.selected {
				fmt.Fprintf(&b, "  %s\n", accent.Render("> "+label))
			} else {
				fmt.Fprintf(&b, "    %s\n", label)
			}
		}
		mode := "Read-only session"
		if m.supports("apply") {
			mode = m.accessMode()
		}
		fmt.Fprintf(&b, "\n%s\n", mode)
		if m.state.runtime.Status != "" {
			fmt.Fprintf(&b, "\nRuntime: %s\nPlan: %s\n", clean(m.state.runtime.Status), clean(m.state.plan.Status))
		}
	case status:
		fmt.Fprintf(&b, "Stack: %s\nManaged state: %v\nApplied baseline: %v\n", clean(m.state.report.Stack), m.state.report.Managed, m.state.report.Baseline)
		for _, reason := range m.state.report.Blockers {
			fmt.Fprintf(&b, "\nBlocked: %s\n", clean(reason))
		}
		fmt.Fprintf(&b, "\nRuntime: %s\n", clean(m.state.runtime.Status))
		for _, check := range m.state.runtime.Checks {
			mark := "ok"
			if !check.OK {
				mark = "blocked"
			}
			fmt.Fprintf(&b, "  [%s] %s\n", mark, clean(check.ID))
			if check.Reason != "" {
				fmt.Fprintf(&b, "       %s\n", clean(check.Reason))
			}
		}
		fmt.Fprintf(&b, "\n%s\n", clean(m.state.runtime.Observation))
	case plan:
		return planContent(m.state.plan)
	case operation:
		op := m.state.operation
		fmt.Fprintf(&b, "Operation: %s\n", clean(op.Status))
		if op.ID != "" {
			fmt.Fprintf(&b, "\nID: %s\nKind: %s\nSteps: %d / %d\n", clean(op.ID), clean(op.Kind), op.Completed, op.Total)
		}
		if op.CurrentStep != "" {
			fmt.Fprintf(&b, "Current step: %s\n", clean(op.CurrentStep))
		}
	case password:
		fmt.Fprintf(&b, "New panel password\n%s\n\nRepeat password\n%s\n\nSaves desired configuration only.\nReview and Apply are separate steps.", m.fields[0].View(), m.fields[1].View())
	case review:
		if m.review == nil {
			return "No active review. Return to Plan to create one."
		}
		fmt.Fprintf(&b, "Review: %s\nExpires: %s\n\n%s", clean(m.review.ID), m.review.ExpiresAt.Local().Format("15:04:05"), planContent(m.review.Plan))
		if !m.review.CanApply {
			b.WriteString("\nThis controller cannot apply the review.\n")
		}
	case confirmation:
		label := map[string]string{"apply": "Apply the reviewed changes?", "resume": "Resume this operation from its saved checkpoint?", "cancel": "Cancel preparation before any deployment effects?"}[m.confirmAction]
		fmt.Fprintf(&b, "%s\n\nID: %s\n\nEnter confirms. Escape goes back.", label, clean(m.confirmID))
	}
	return b.String()
}
