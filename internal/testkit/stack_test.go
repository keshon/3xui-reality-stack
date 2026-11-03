package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func TestFailureMatrixAndFreshProcessResume(t *testing.T) {
	for _, fault := range []Fault{BeforeWrite, AfterWrite, DuringVerify} {
		t.Run(string(fault), func(t *testing.T) {
			root := t.TempDir()
			if err := Seed(root, "private-before", "private-after"); err != nil {
				t.Fatal(err)
			}
			s := Stack{Fault: fault}
			w, err := s.Review(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			e := deployment.Engine{Root: root}
			err = e.Start(context.Background(), w)
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("expected a sanitized failure")
			}
			applied, _ := read(root, "applied.json")
			if !strings.Contains(string(applied), "private-before") {
				t.Fatal("unverified state was committed")
			}
			if err := e.Resume(context.Background(), (Stack{}).Resume(root)); err != nil {
				t.Fatal(err)
			}
			i, err := (Stack{}).Inspect(context.Background(), root)
			if err != nil || i.Plan.Status != "unchanged" {
				t.Fatal("fixture did not converge")
			}
			log, err := os.ReadFile(filepath.Join(root, ".fixture", "effects.log"))
			if err != nil || string(log) != "configure-site\n" {
				t.Fatal("recovery repeated an already applied effect")
			}
		})
	}
}

func TestFixtureAbruptExitHelper(t *testing.T) {
	root := os.Getenv("STACK_FIXTURE_CRASH_ROOT")
	if root == "" {
		return
	}
	s := Stack{AfterPersist: func() { os.Exit(23) }}
	w, err := s.Review(context.Background(), root)
	if err != nil {
		os.Exit(24)
	}
	_ = (deployment.Engine{Root: root}).Start(context.Background(), w)
	os.Exit(25)
}

func TestAbruptExitLeavesIntentAndRecoversWithoutDuplicateEffect(t *testing.T) {
	root := t.TempDir()
	if err := Seed(root, "old", "new"); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestFixtureAbruptExitHelper$")
	cmd.Env = append(os.Environ(), "STACK_FIXTURE_CRASH_ROOT="+root)
	err = cmd.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 23 {
		t.Fatal("helper did not exit at the intended failure boundary")
	}
	data, err := os.ReadFile(filepath.Join(root, ".stack", "go-operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var marker struct {
		Status string `json:"status"`
		Next   int    `json:"next"`
	}
	if json.Unmarshal(data, &marker) != nil || marker.Status != "running" || marker.Next != 0 {
		t.Fatal("process exit did not preserve action intent")
	}
	if err := (deployment.Engine{Root: root}).Resume(ctx, (Stack{}).Resume(root)); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(root, ".fixture", "effects.log"))
	if err != nil || string(data) != "configure-site\n" {
		t.Fatal("abrupt-exit recovery duplicated effect")
	}
}

func TestStaleReviewAndExternalDrift(t *testing.T) {
	root := t.TempDir()
	if err := Seed(root, "old", "new"); err != nil {
		t.Fatal(err)
	}
	s := Stack{}
	w, err := s.Review(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := deployment.WriteJSON(filepath.Join(root, ".fixture", "desired.json"), state{"changed"}); err != nil {
		t.Fatal(err)
	}
	e := deployment.Engine{Root: root}
	if e.Start(context.Background(), w) == nil {
		t.Fatal("stale review applied")
	}
	if err := e.CancelPreparation(); err != nil {
		t.Fatal(err)
	}
	if err := deployment.WriteJSON(filepath.Join(root, ".fixture", "running.json"), state{"external-change"}); err != nil {
		t.Fatal(err)
	}
	i, err := s.Inspect(context.Background(), root)
	if err != nil || i.Plan.Status != "blocked" {
		t.Fatal("external runtime drift was ignored")
	}
}
