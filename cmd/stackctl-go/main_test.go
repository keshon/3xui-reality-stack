package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/buildinfo"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/testkit"
)

func TestCLIInitializePreparesNewRootWithoutDeployment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "draft")
	input := `{"schema_version":1,"base_domain":"example.test","public_domain":"www.example.test","panel_domain":"ops.example.test","acme_email":"admin@example.test","users":["alice"]}`
	args := []string{"initialize", "--root", root, "--config-stdin"}
	var out bytes.Buffer
	if err := runContext(context.Background(), args, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	var report deployment.InitializationReport
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Status != "prepared" || report.CanApply || strings.Contains(out.String(), "alice") {
		t.Fatal("initialization CLI claimed deployment or exposed input")
	}
	if _, err := os.Stat(filepath.Join(root, ".stack/config.json")); err != nil {
		t.Fatal("initialization CLI did not save desired configuration")
	}
	if _, err := os.Stat(filepath.Join(root, ".stack/applied.json")); !os.IsNotExist(err) {
		t.Fatal("initialization CLI created applied state")
	}
	if err := runContext(context.Background(), []string{"initialize", "--root", root}, strings.NewReader(input), &out); err == nil {
		t.Fatal("initialization accepted missing stdin flag")
	}
}

type preflightFixture struct {
	testkit.Stack
	status string
}

func (s preflightFixture) Preflight(ctx context.Context, root string) (deployment.PreflightReport, error) {
	return deployment.PreflightReport{Stack: "fixture-site", Status: s.status, Installation: "empty", Checks: []deployment.Check{}}, ctx.Err()
}

func TestCLIPreflightReportsBlockedFreshDeployment(t *testing.T) {
	for _, status := range []string{"candidate", "blocked"} {
		root := filepath.Join(t.TempDir(), "absent")
		var out bytes.Buffer
		err := runWithStack(context.Background(), []string{"preflight", "--root", root}, &out, preflightFixture{status: status})
		if (err != nil) != (status == "blocked") {
			t.Fatal("preflight exit status does not match report")
		}
		var report deployment.PreflightReport
		if json.Unmarshal(out.Bytes(), &report) != nil || report.Status != status {
			t.Fatal("preflight JSON report was lost")
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("preflight created deployment state")
		}
	}
}

func TestVersionRequiresNoDeploymentOrInput(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var out bytes.Buffer
		if err := runContext(context.Background(), []string{arg}, nil, &out); err != nil || out.String() != buildinfo.Version+"\n" {
			t.Fatal("version command required configuration or reported the wrong build")
		}
	}
}

func TestCLIConfigurePasswordSavesOnlyDesiredField(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".stack"), 0700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"schema_version": 1, "env": map[string]string{"PANEL_USER": "admin", "PANEL_PASS": "old-password"}, "users": []string{}, "nodes": []any{}}
	hashes := map[string]string{}
	for _, name := range []string{".env", "users.txt", "nodes.txt"} {
		data := []byte("old-generated-" + name)
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		hashes[name] = hex.EncodeToString(sum[:])
	}
	for name, value := range map[string]any{"config.json": config, "applied.json": map[string]any{"version": 1, "config": config, "output_hashes": hashes}} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(root, ".stack", name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	baseline, _ := os.ReadFile(filepath.Join(root, ".stack", "applied.json"))
	args := []string{"configure-password", "--root", root, "--password-stdin", "--experimental-password-writes"}
	literal := `private $HOME '"\ пароль & < >`
	var out bytes.Buffer
	if err := runContext(context.Background(), args, strings.NewReader(literal+"\r\n"), &out); err != nil {
		t.Fatal(err)
	}
	var plan deployment.Plan
	if json.Unmarshal(out.Bytes(), &plan) != nil || plan.Status != "proposed" || strings.Contains(out.String(), "private") {
		t.Fatal("password command did not return a secret-free plan")
	}
	var saved struct {
		Env map[string]string `json:"env"`
	}
	data, _ := os.ReadFile(filepath.Join(root, ".stack", "config.json"))
	if json.Unmarshal(data, &saved) != nil || saved.Env["PANEL_PASS"] != literal {
		t.Fatal("CLI password literal was not saved")
	}
	after, _ := os.ReadFile(filepath.Join(root, ".stack", "applied.json"))
	if !bytes.Equal(baseline, after) {
		t.Fatal("CLI configuration edit changed applied state")
	}
	for _, args := range [][]string{
		{"configure-password", "--root", root, "--password-stdin"},
		{"configure-password", "--root", root, "--experimental-password-writes"},
		{"plan", "--root", root, "--password-stdin"},
	} {
		if err := runContext(context.Background(), args, strings.NewReader("private"), &out); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid password command accepted or echoed input")
		}
	}
}

func TestPasswordWritesAreAnExplicitControllerCapability(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		args := []string{"serve", "--stdio", "--root", t.TempDir()}
		if enabled {
			args = append(args, "--experimental-password-writes")
		}
		var out bytes.Buffer
		input := strings.NewReader("{\"version\":1,\"id\":1,\"method\":\"hello\"}\n")
		if err := runContext(context.Background(), args, input, &out); err != nil {
			t.Fatal(err)
		}
		var reply struct {
			Result struct {
				ReadOnly bool     `json:"read_only"`
				Methods  []string `json:"methods"`
			} `json:"result"`
		}
		if json.Unmarshal(out.Bytes(), &reply) != nil || reply.Result.ReadOnly == enabled || slices.Contains(reply.Result.Methods, "apply") != enabled || slices.Contains(reply.Result.Methods, "configure") != enabled {
			t.Fatal("controller password capability does not match explicit selection")
		}
	}
	var out bytes.Buffer
	if err := run([]string{"inspect", "--experimental-password-writes"}, &out); err == nil {
		t.Fatal("irrelevant password execution option was accepted")
	}
}

func TestInspectUnmanagedAndPlanExit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	var out bytes.Buffer
	if err := run([]string{"inspect", "--root", root}, &out); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Managed bool `json:"managed"`
	}
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Managed {
		t.Fatal("invalid inspect output")
	}
	out.Reset()
	if err := run([]string{"plan", "--root", root}, &out); err == nil {
		t.Fatal("blocked plan succeeded")
	}
	if !json.Valid(out.Bytes()) {
		t.Fatal("plan output is not JSON")
	}
}

func TestCLIReviewApplyAndOperationUseExactIdentifiers(t *testing.T) {
	root := t.TempDir()
	if err := testkit.Seed(root, "old", "new"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ctx := context.Background()
	if err := runWithStack(ctx, []string{"review", "--root", root}, &out, testkit.Stack{}); err != nil {
		t.Fatal(err)
	}
	var review deployment.Review
	if json.Unmarshal(out.Bytes(), &review) != nil || !review.CanApply {
		t.Fatal("invalid CLI review")
	}
	out.Reset()
	if err := runWithStack(ctx, []string{"apply", "--root", root, "--confirm", review.ID}, &out, testkit.Stack{}); err != nil {
		t.Fatal(err)
	}
	var status deployment.OperationStatus
	if json.Unmarshal(out.Bytes(), &status) != nil || status.Status != "complete" {
		t.Fatal("CLI apply did not complete")
	}
	out.Reset()
	if err := runWithStack(ctx, []string{"resume", "--root", root, "--operation", status.ID}, &out, testkit.Stack{}); err != nil {
		t.Fatal(err)
	}
	if err := runWithStack(ctx, []string{"plan", "--root", root, "--confirm", review.ID}, &out, testkit.Stack{}); err == nil {
		t.Fatal("irrelevant confirmation accepted")
	}
}

func TestCLIUsesSecondStackThroughSameCompositionBoundary(t *testing.T) {
	root := t.TempDir()
	if err := testkit.Seed(root, "private-old", "private-new"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runWithStack(context.Background(), []string{"plan", "--root", root}, &out, testkit.Stack{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fixture-site") || strings.Contains(out.String(), "private-") {
		t.Fatal("CLI assumes a panel stack or exposes fixture values")
	}
	out.Reset()
	input := strings.NewReader("{\"version\":1,\"id\":1,\"method\":\"hello\"}\n{\"version\":1,\"id\":2,\"method\":\"plan\"}\n")
	if err := runApplication(context.Background(), []string{"serve", "--stdio", "--root", root}, input, &out, testkit.Stack{}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\n") != 2 || !strings.Contains(out.String(), "fixture-site") {
		t.Fatal("stdio entrypoint polluted or lost protocol output")
	}
}

func TestUnsupportedCommandsCannotExecute(t *testing.T) {
	for _, args := range [][]string{{}, {"apply"}, {"inspect", "unexpected"}, {"plan", "--private-secret"}} {
		var out bytes.Buffer
		err := run(args, &out)
		if err == nil {
			t.Fatal("invalid command accepted")
		}
		if strings.Contains(err.Error(), "private-secret") {
			t.Fatal("arguments leaked in error")
		}
	}
}

func TestCLIInstallationOptInIsLocalAndSeparate(t *testing.T) {
	for _, args := range [][]string{
		{"serve", "--stdio", "--experimental-install"},
		{"tui", "--experimental-install"},
		{"initialize", "--config-stdin", "--experimental-install"},
		{"review", "--experimental-install", "--experimental-password-writes"},
	} {
		var out bytes.Buffer
		if err := runContext(t.Context(), args, nil, &out); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("invalid installation option combination accepted: %v", args)
		}
	}
	root := t.TempDir()
	for _, enabled := range []bool{false, true} {
		args := []string{"apply", "--root", root, "--confirm", strings.Repeat("a", 32)}
		if enabled {
			args = append(args, "--experimental-install")
		}
		var out bytes.Buffer
		err := runContext(t.Context(), args, nil, &out)
		if err == nil || (err == deployment.ErrExecutionUnavailable) == enabled {
			t.Fatal("CLI did not select only the explicitly enabled installation adapter", err)
		}
	}
}
