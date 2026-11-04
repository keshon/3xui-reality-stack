package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type accessBackend struct {
	fakeBackend
	accessCalls int
	failAccess  bool
}

func (b *accessBackend) Methods() []string { return append(b.fakeBackend.Methods(), "access") }
func (b *accessBackend) Access(context.Context) (deployment.AccessReport, error) {
	b.accessCalls++
	if b.failAccess {
		return deployment.AccessReport{}, errors.New("do-not-display-password")
	}
	return deployment.AccessReport{Stack: "fixture", Links: []deployment.AccessValue{{Label: "Panel", Value: "https://ops.example.test/panel-generated/"}}, Credentials: []deployment.AccessValue{{Label: "Admin password", Value: "private-password", Secret: true}}}, nil
}

func TestAccessSurvivesReopeningButSecretsDoNotSurviveNavigation(t *testing.T) {
	b := &accessBackend{fakeBackend: fakeBackend{p: deployment.Plan{Stack: "fixture", Status: "unchanged", ReadOnly: true}}}
	for range 2 {
		m := newModel(t.Context(), b, "Installed stack")
		finish(m, m.Init())
		if !strings.Contains(m.navigation(), "Access") || !m.available("access") {
			t.Fatal("reopened read-only installation lost access page")
		}
		finish(m, m.dispatch("access"))
		if !strings.Contains(m.content(), "/panel-generated/") || strings.Contains(m.View().Content, "private-password") {
			t.Fatal("access page omitted full URL or displayed a password by default")
		}
		clickLabel(t, m, "v Show password")
		if !strings.Contains(m.View().Content, "private-password") {
			t.Fatal("explicit mouse reveal did not work")
		}
		m.dispatch("help")
		if strings.Contains(m.View().Content, "private-password") {
			t.Fatal("help left a revealed password visible behind the modal")
		}
		press(m, "esc")
		m.dispatch("home")
		if m.accessInfo != nil || m.showSecrets || strings.Contains(m.View().Content, "private-password") {
			t.Fatal("navigation retained visible private access details")
		}
	}
	b.failAccess = true
	m := newModel(t.Context(), b, "Installed stack")
	finish(m, m.Init())
	finish(m, m.dispatch("access"))
	if strings.Contains(m.View().Content, "do-not-display-password") || m.accessInfo != nil {
		t.Fatal("failed access read leaked adapter output")
	}
}

func TestCompletedInstallationOpensAccessAfterStateRefresh(t *testing.T) {
	b := &accessBackend{fakeBackend: fakeBackend{p: deployment.Plan{Stack: "fixture", Status: "unchanged", ReadOnly: true}, op: deployment.OperationStatus{Status: "complete", Kind: "xui-install-v1", Completed: 7, Total: 7}}}
	m := newModel(t.Context(), b, "Installed stack")
	_, cmd := m.Update(actionResult{action: "apply", operation: b.op})
	finish(m, cmd)
	if m.screen != access || b.accessCalls != 1 || !strings.Contains(m.content(), "Installation complete") {
		t.Fatal("completed deployment did not hand off to access details")
	}
	m.dispatch("toggle-secrets")
	finish(m, press(m, "q"))
	if m.showSecrets || m.accessInfo != nil || strings.Contains(m.View().Content, "private-password") {
		t.Fatal("quit left private access data in final frame")
	}
}
