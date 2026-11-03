package control_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/testkit"
)

type configSpy struct {
	testkit.Stack
	calls *int
	fail  bool
}

func (s configSpy) Configure(ctx context.Context, root string, changes map[string]string) (deployment.Plan, error) {
	*s.calls++
	if s.fail {
		return deployment.Plan{}, errors.New("private-password-in-adapter-error")
	}
	return deployment.Plan{Stack: "fixture-site", Status: "proposed", ReadOnly: true}, nil
}

func TestConfigureProtocolRejectsExtraFieldsAndMalformedChanges(t *testing.T) {
	for _, params := range []string{
		`{}`, `null`, `{"changes":null}`, `{"changes":{}}`,
		`{"changes":{"PANEL_PASS":"private-value"},"root":"/unreviewed"}`,
		`{"changes":["private-value"]}`,
		`{"changes":{"a":"1","b":"1","c":"1","d":"1","e":"1","f":"1","g":"1","h":"1","i":"1","j":"1","k":"1","l":"1","m":"1","n":"1","o":"1","p":"1","q":"1"}}`,
	} {
		calls := 0
		controller := control.Controller{Root: t.TempDir(), Stack: configSpy{calls: &calls}}
		input := `{"version":1,"id":1,"method":"configure","params":` + params + "}\n"
		var out bytes.Buffer
		if err := control.Serve(context.Background(), strings.NewReader(input), &out, controller); err != nil {
			t.Fatal(err)
		}
		if calls != 0 || !strings.Contains(out.String(), "invalid_params") || strings.Contains(out.String(), "private-value") || strings.Contains(out.String(), "unreviewed") {
			t.Fatal("invalid configuration request reached adapter or exposed input")
		}
	}
}

func TestConfigureErrorsNeverExposeAdapterSecrets(t *testing.T) {
	calls := 0
	controller := control.Controller{Root: t.TempDir(), Stack: configSpy{calls: &calls, fail: true}}
	if _, err := controller.Configure(context.Background(), map[string]string{"PANEL_PASS": "private-value"}); err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatal("local configuration error exposed adapter input")
	}
	client, ctx := connectController(t, controller)
	if _, err := client.Configure(ctx, map[string]string{"PANEL_PASS": "private-value"}); err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatal("remote configuration error exposed adapter input")
	}
}

func TestConfigureRequiresAdvertisedCapability(t *testing.T) {
	client, ctx := connectController(t, control.Controller{Root: t.TempDir(), Stack: testkit.Stack{}})
	if _, err := client.Configure(ctx, map[string]string{"PANEL_PASS": "private-value"}); !errors.Is(err, deployment.ErrExecutionUnavailable) {
		t.Fatal("client attempted an unsupported configuration method")
	}
	var public bytes.Buffer
	controller := control.Controller{Root: t.TempDir(), Stack: testkit.Stack{}}
	input, _ := json.Marshal(map[string]any{"version": 1, "id": 1, "method": "configure", "params": map[string]any{"changes": map[string]string{"PANEL_PASS": "private-value"}}})
	if err := control.Serve(context.Background(), bytes.NewReader(append(input, '\n')), &public, controller); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(public.String(), "unsupported_method") || strings.Contains(public.String(), "private-value") {
		t.Fatal("unadvertised configuration method accepted or echoed input")
	}
}
