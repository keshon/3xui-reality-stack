package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/certificates"
)

func TestCertificateCommandWaitsWithoutCreatingRuntimeState(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "output-not-created")
	args := []string{"export-certificates", "--source", filepath.Join(parent, "pending-acme.json"), "--destination", destination, "--domain", "example.test"}
	var output bytes.Buffer
	if err := runContext(t.Context(), args, nil, &output); err != nil {
		t.Fatal(err)
	}
	var report certificates.Report
	if json.Unmarshal(output.Bytes(), &report) != nil || report.Status != "waiting" || report.ChangedFiles != 0 {
		t.Fatal("one-shot exporter lost pending status")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("waiting command created destination")
	}
	for _, suffix := range [][]string{{"--interval", "2s"}, {"--watch", "--interval", "0s"}, {"--root", "private-argument-marker"}, {"unexpected-private-value"}} {
		output.Reset()
		err := runContext(t.Context(), append(append([]string{}, args...), suffix...), nil, &output)
		if err == nil || strings.Contains(err.Error(), "private-argument-marker") || strings.Contains(err.Error(), "unexpected-private-value") || output.Len() != 0 {
			t.Fatal("invalid certificate flags were accepted or echoed")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output.Reset()
	if err := runContext(ctx, append(args, "--watch"), nil, &output); err != nil || output.Len() != 0 {
		t.Fatal("watch did not cancel cleanly")
	}
}
