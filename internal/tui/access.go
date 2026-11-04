package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type accessResult struct {
	report deployment.AccessReport
	err    error
}

func (m *model) loadAccess() tea.Cmd {
	m.accessInfo, m.showSecrets = nil, false
	m.busy = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		r, err := m.backend.(control.AccessReader).Access(ctx)
		return accessResult{r, err}
	}
}

func (m *model) accessContent() string {
	if m.accessInfo == nil {
		return "Access details are unavailable. Refresh or inspect installation state."
	}
	var b strings.Builder
	if m.state.operation.Kind == "xui-install-v1" && m.state.operation.Status == "complete" {
		b.WriteString("Installation complete\n\n")
	}
	for _, section := range [][]deployment.AccessValue{m.accessInfo.Links, m.accessInfo.Credentials} {
		for _, field := range section {
			value := field.Value
			if field.Secret && !m.showSecrets {
				value = "[hidden]"
			}
			fmt.Fprintf(&b, "%s\n%s\n\n", clean(field.Label), clean(value))
		}
	}
	fmt.Fprintf(&b, "Configuration\n%s\n\n", clean(m.accessInfo.ConfigFile))
	for i, instruction := range m.accessInfo.Instructions {
		fmt.Fprintf(&b, "%d. %s\n", i+1, clean(instruction))
	}
	return b.String()
}
