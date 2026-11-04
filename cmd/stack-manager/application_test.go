package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/buildinfo"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/sshclient"
	"github.com/keshon/3xui-reality-stack/internal/testkit"
)

type initializationFixture struct {
	testkit.Stack
	input chan json.RawMessage
}

func (s initializationFixture) Initialize(ctx context.Context, root string, input json.RawMessage) (deployment.InitializationReport, error) {
	s.input <- append(json.RawMessage(nil), input...)
	return deployment.InitializationReport{Stack: "fixture-site", Status: "prepared", Files: []string{"desired.json"}, CanApply: false}, nil
}

func TestManagerInitializationKeepsInputOnPrivateChannel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := make(chan json.RawMessage, 1)
	done := make(chan struct{})
	connect := func(ctx context.Context, options sshclient.Options) (io.ReadWriteCloser, error) {
		if options.ExperimentalPasswordWrites {
			t.Fatal("draft initialization enabled runtime writes")
		}
		left, right := net.Pipe()
		go func() {
			defer close(done)
			defer right.Close()
			_ = control.Serve(ctx, right, right, control.Controller{Root: "/opt/new-stack", Stack: initializationFixture{input: input}})
		}()
		return left, nil
	}
	raw := `{"schema_version":1,"private_value":"private-password"}`
	var out bytes.Buffer
	args := []string{"initialize", "--ssh", "test-vps", "--root", "/opt/new-stack", "--config-stdin"}
	if err := runWithInput(ctx, args, strings.NewReader(raw), &out, connect); err != nil {
		t.Fatal(err)
	}
	<-done
	if string(<-input) != raw || strings.Contains(out.String(), "private-") {
		t.Fatal("manager changed initialization input or exposed a secret")
	}
}

func TestManagerVersionNeverStartsSSH(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var out bytes.Buffer
		connect := func(context.Context, sshclient.Options) (io.ReadWriteCloser, error) {
			t.Fatal("version started SSH")
			return nil, nil
		}
		if err := run(context.Background(), []string{arg}, &out, connect); err != nil || out.String() != buildinfo.Version+"\n" {
			t.Fatal("manager reported the wrong build version")
		}
	}
}

type configurationFixture struct {
	testkit.Stack
	changes chan map[string]string
}

func (s configurationFixture) Configure(ctx context.Context, root string, changes map[string]string) (deployment.Plan, error) {
	s.changes <- changes
	return deployment.Plan{Stack: "fixture-site", Status: "proposed", ReadOnly: true}, nil
}

func TestManagerConfiguresPasswordOverPrivateProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	changes := make(chan map[string]string, 1)
	done := make(chan struct{})
	connect := func(ctx context.Context, options sshclient.Options) (io.ReadWriteCloser, error) {
		if !options.ExperimentalPasswordWrites {
			t.Fatal("manager did not select password configuration capability")
		}
		left, right := net.Pipe()
		go func() {
			defer close(done)
			defer right.Close()
			_ = control.Serve(ctx, right, right, control.Controller{Root: t.TempDir(), Stack: configurationFixture{changes: changes}})
		}()
		return left, nil
	}
	literal := `private $HOME '"\ пароль`
	args := []string{"configure-password", "--ssh", "test-vps", "--root", "/opt/stack", "--password-stdin", "--experimental-password-writes"}
	var out bytes.Buffer
	if err := runWithInput(ctx, args, strings.NewReader(literal+"\n"), &out, connect); err != nil {
		t.Fatal(err)
	}
	<-done
	if got := <-changes; len(got) != 1 || got["PANEL_PASS"] != literal {
		t.Fatal("manager changed the password or sent unsupported fields")
	}
	if strings.Contains(out.String(), "private") {
		t.Fatal("manager printed password input")
	}
	called := false
	connect = func(context.Context, sshclient.Options) (io.ReadWriteCloser, error) {
		called = true
		return nil, nil
	}
	if err := runWithInput(ctx, args, strings.NewReader("private\nsecond-line"), &out, connect); err == nil || called || strings.Contains(err.Error(), "private") {
		t.Fatal("invalid password input reached SSH or was echoed")
	}
}

func TestManagerForwardsPasswordOptInOnlyWhenRequested(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		args := []string{"review", "--ssh", "test-vps", "--root", "/opt/stack"}
		if enabled {
			args = append(args, "--experimental-password-writes")
		}
		called := false
		connect := func(ctx context.Context, o sshclient.Options) (io.ReadWriteCloser, error) {
			called = true
			if o.ExperimentalPasswordWrites != enabled {
				t.Fatal("manager changed the operator's execution selection")
			}
			return nil, errors.New("fixture stops before SSH")
		}
		var out bytes.Buffer
		if err := run(context.Background(), args, &out, connect); err == nil || !called {
			t.Fatal("manager did not reach the configured transport")
		}
	}
	called := false
	connect := func(context.Context, sshclient.Options) (io.ReadWriteCloser, error) {
		called = true
		return nil, nil
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"plan", "--ssh", "test-vps", "--root", "/opt/stack", "--experimental-password-writes"}, &out, connect); err == nil || called {
		t.Fatal("irrelevant execution choice started SSH")
	}
}

func TestManagerPlansThroughControllerWithNoStackKnowledge(t *testing.T) {
	root := t.TempDir()
	if err := testkit.Seed(root, "private-old", "private-new"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	connect := func(ctx context.Context, options sshclient.Options) (io.ReadWriteCloser, error) {
		if !reflect.DeepEqual(options, sshclient.Options{Alias: "test-vps", Root: "/opt/my stack", Controller: "/opt/bin/stackctl-go"}) {
			t.Fatal("manager connection options changed")
		}
		left, right := net.Pipe()
		go func() {
			defer close(done)
			defer right.Close()
			_ = control.Serve(ctx, right, right, control.Controller{Root: root, Stack: testkit.Stack{}})
		}()
		return left, nil
	}
	var out bytes.Buffer
	err := run(ctx, []string{"plan", "--ssh", "test-vps", "--root", "/opt/my stack", "--controller", "/opt/bin/stackctl-go"}, &out, connect)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	var plan deployment.Plan
	if json.Unmarshal(out.Bytes(), &plan) != nil || plan.Stack != "fixture-site" || plan.Status != "proposed" {
		t.Fatal("manager did not render remote plan")
	}
	if strings.Contains(out.String(), "private-") {
		t.Fatal("fixture configuration leaked")
	}
}

func TestManagerRejectsUnsupportedCommandsBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{{}, {"apply"}, {"plan"}, {"plan", "--ssh", "test-vps"}, {"inspect", "--private-secret"}} {
		called := false
		connect := func(context.Context, sshclient.Options) (io.ReadWriteCloser, error) { called = true; return nil, nil }
		var out bytes.Buffer
		err := run(context.Background(), args, &out, connect)
		if err == nil || called || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("invalid manager command connected or echoed arguments")
		}
	}
}

func TestManagerProductionDependenciesExcludeStackAdaptersAndMocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal("cannot inspect production dependency graph")
	}
	for _, forbidden := range []string{"/internal/xui", "/internal/testkit"} {
		if strings.Contains(string(out), forbidden) {
			t.Fatal("manager binary imports a stack adapter or simulated stack")
		}
	}
}

func TestManagerReviewAndConfirmedApplyUseSameRemoteController(t *testing.T) {
	root := t.TempDir()
	if err := testkit.Seed(root, "old", "new"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connect := func(ctx context.Context, options sshclient.Options) (io.ReadWriteCloser, error) {
		left, right := net.Pipe()
		go func() {
			defer right.Close()
			_ = control.Serve(ctx, right, right, control.Controller{Root: root, Stack: testkit.Stack{}})
		}()
		return left, nil
	}
	var out bytes.Buffer
	if err := run(ctx, []string{"review", "--ssh", "test-vps", "--root", "/opt/stack"}, &out, connect); err != nil {
		t.Fatal(err)
	}
	var review deployment.Review
	if json.Unmarshal(out.Bytes(), &review) != nil {
		t.Fatal("invalid manager review")
	}
	out.Reset()
	if err := run(ctx, []string{"apply", "--ssh", "test-vps", "--root", "/opt/stack", "--confirm", review.ID}, &out, connect); err != nil {
		t.Fatal(err)
	}
	var status deployment.OperationStatus
	if json.Unmarshal(out.Bytes(), &status) != nil || status.Status != "complete" {
		t.Fatal("manager apply did not complete")
	}
}
