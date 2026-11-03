package certificates

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadinessRequiresExportedCurrentPairAndAllHostnames(t *testing.T) {
	e, source, destination := fixture(t)
	hosts := []string{"www.example.test", "ops.example.test"}
	if ready, err := e.Ready(t.Context(), hosts); err != nil || ready {
		t.Fatal("pending issuance claimed readiness")
	}
	c := testCertificate(t, "example.test", []string{"example.test", "*.example.test"}, false)
	writeState(t, source, c)
	if ready, err := e.Ready(t.Context(), hosts); err != nil || ready {
		t.Fatal("unexported certificate claimed readiness")
	}
	if _, err := e.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ready, err := e.Ready(t.Context(), hosts); err != nil || !ready {
		t.Fatal("valid exported wildcard was not ready")
	}
	if ready, err := e.Ready(t.Context(), []string{"outside.example.net"}); err == nil || ready {
		t.Fatal("uncovered hostname claimed readiness")
	}
	renewed := testCertificate(t, "example.test", []string{"example.test", "*.example.test"}, false)
	writeState(t, source, renewed)
	if ready, err := e.Ready(t.Context(), hosts); err != nil || ready {
		t.Fatal("stale exported pair claimed readiness")
	}
	if _, err := e.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ready, err := e.Ready(t.Context(), hosts); err != nil || !ready {
		t.Fatal("renewed exported pair was not ready")
	}
	if err := os.WriteFile(filepath.Join(destination, "private/example.test.key"), c.Key, 0600); err != nil {
		t.Fatal(err)
	}
	if ready, err := e.Ready(t.Context(), hosts); err != nil || ready {
		t.Fatal("mixed exported pair claimed readiness")
	}
}
