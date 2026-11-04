package certificates

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func testCertificate(t *testing.T, main string, names []string, expired bool) certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	until := now.Add(time.Hour)
	if expired {
		until = now.Add(-time.Minute)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: main}, DNSNames: names, NotBefore: now.Add(-time.Hour), NotAfter: until, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	c := certificate{Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), Key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})}
	c.Domain.Main = main
	return c
}

func fixture(t *testing.T) (*Exporter, string, string) {
	t.Helper()
	parent := t.TempDir()
	source, destination := filepath.Join(parent, "acme.json"), filepath.Join(parent, "certificates")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	e, err := New(Options{Source: source, Destination: destination, Resolver: "le", Domain: "example.test"})
	if err != nil {
		t.Fatal(err)
	}
	return e, source, destination
}

func writeState(t *testing.T, source string, certs ...certificate) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"le": map[string]any{"Account": map[string]string{"PrivateKey": "unrelated-account-secret"}, "Certificates": certs}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func pairBytes(t *testing.T, destination string) (cert, key []byte) {
	t.Helper()
	var err error
	cert, err = os.ReadFile(filepath.Join(destination, "certs/example.test.crt"))
	if err != nil {
		t.Fatal(err)
	}
	key, err = os.ReadFile(filepath.Join(destination, "private/example.test.key"))
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestExportRenewalIsValidatedAndUnchangedFilesArePreserved(t *testing.T) {
	e, source, destination := fixture(t)
	for _, main := range []string{"example.test", "*.example.test"} {
		c := testCertificate(t, main, []string{main}, false)
		writeState(t, source, c)
		report, err := e.Export(t.Context())
		if err != nil || report.Status != "ready" || report.ChangedFiles != 2 {
			t.Fatal("valid certificate publication failed")
		}
		cert, key := pairBytes(t, destination)
		if !bytes.Equal(cert, c.Certificate) || !bytes.Equal(key, c.Key) {
			t.Fatal("published pair differs from ACME state")
		}
		if _, err := tls.X509KeyPair(cert, key); err != nil {
			t.Fatal("published pair mismatches")
		}
		path := filepath.Join(destination, "certs/example.test.crt")
		old := time.Now().Add(-time.Hour).Truncate(time.Second)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		report, err = e.Export(t.Context())
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil || report.ChangedFiles != 0 || !info.ModTime().Equal(old) {
			t.Fatal("unchanged export rewrote certificate")
		}
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{"certs": 0755, "private": 0700, "certs/example.test.crt": 0644, "private/example.test.key": 0600} {
			info, err := os.Stat(filepath.Join(destination, path))
			if err != nil || info.Mode().Perm() != mode {
				t.Fatal("certificate output permissions differ")
			}
		}
	}
	for _, dir := range []string{"certs", "private"} {
		entries, err := os.ReadDir(filepath.Join(destination, dir))
		if err != nil || len(entries) != 1 {
			t.Fatal("publication left temporary or unrelated certificate files")
		}
	}
}

func TestInvalidACMEStateNeverReplacesLastPair(t *testing.T) {
	e, source, destination := fixture(t)
	c := testCertificate(t, "example.test", []string{"example.test"}, false)
	other := testCertificate(t, "example.test", []string{"example.test"}, false)
	writeState(t, source, c)
	if _, err := e.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	beforeCert, beforeKey := pairBytes(t, destination)
	for _, scenario := range []string{"truncated", "invalid-base64", "mismatched", "wrong-host", "expired", "duplicate", "path-traversal", "declared-san"} {
		t.Run(scenario, func(t *testing.T) {
			bad := c
			switch scenario {
			case "mismatched":
				bad.Key = other.Key
			case "wrong-host":
				bad = testCertificate(t, "example.test", []string{"unrelated.test"}, false)
			case "expired":
				bad = testCertificate(t, "example.test", []string{"example.test"}, true)
			case "declared-san":
				bad.Domain.SANs = []string{"unrelated.test"}
			case "path-traversal":
				bad.Domain.Main = "../../private"
			}
			writeState(t, source, bad)
			if scenario == "truncated" {
				os.WriteFile(source, []byte(`{"le":{"Certificates":"private-error-marker`), 0600)
			}
			if scenario == "invalid-base64" {
				os.WriteFile(source, []byte(`{"le":{"Certificates":[{"certificate":"private-error-marker!"}]}}`), 0600)
			}
			if scenario == "duplicate" {
				writeState(t, source, c, c)
			}
			report, err := e.Export(t.Context())
			if scenario == "path-traversal" {
				if err != nil || report.Status != "waiting" {
					t.Fatal("unselected domain was exported")
				}
			} else if err == nil {
				t.Fatal("invalid ACME state accepted")
			}
			if err != nil && strings.Contains(err.Error(), "private-error-marker") {
				t.Fatal("parse error exposed private state")
			}
			afterCert, afterKey := pairBytes(t, destination)
			if !bytes.Equal(beforeCert, afterCert) || !bytes.Equal(beforeKey, afterKey) {
				t.Fatal("invalid state replaced last certificate pair")
			}
		})
	}
}

func TestInterruptedPairPublicationIsRepairedOnRetry(t *testing.T) {
	e, source, destination := fixture(t)
	old := testCertificate(t, "example.test", []string{"example.test"}, false)
	writeState(t, source, old)
	if _, err := e.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	renewed := testCertificate(t, "example.test", []string{"example.test"}, false)
	writeState(t, source, renewed)
	root, err := os.OpenRoot(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// Reproduce a process stopping between the two per-file commits.
	key, err := stageFile(root, "private/example.test.key", renewed.Key, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer key.cleanup()
	if _, err := key.commit(); err != nil {
		t.Fatal(err)
	}
	report, err := e.Export(t.Context())
	if err != nil || report.ChangedFiles != 1 {
		t.Fatal("interrupted pair did not resume")
	}
	cert, private := pairBytes(t, destination)
	if !bytes.Equal(cert, renewed.Certificate) || !bytes.Equal(private, renewed.Key) {
		t.Fatal("renewal retry left a mixed pair")
	}
}

func TestExportWaitingCancellationAndExclusiveLock(t *testing.T) {
	e, source, destination := fixture(t)
	for _, state := range []string{"missing", "", "{}", "null", `{"le":{"Certificates":[]}}`} {
		if state != "missing" {
			os.WriteFile(source, []byte(state), 0600)
		}
		report, err := e.Export(t.Context())
		if err != nil || report.Status != "waiting" {
			t.Fatal("pending ACME issuance did not wait")
		}
		entries, _ := os.ReadDir(destination)
		if len(entries) != 0 {
			t.Fatal("waiting exporter created output state")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.Export(ctx); err != context.Canceled {
		t.Fatal("export ignored cancellation")
	}
	writeState(t, source, testCertificate(t, "example.test", []string{"example.test"}, false))
	unlock, err := deployment.Lock(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := e.Export(t.Context()); err == nil {
		t.Fatal("concurrent export ignored output lock")
	}
}

func TestWatchRetriesAndReportsWithoutPrivateValues(t *testing.T) {
	e, source, _ := fixture(t)
	os.WriteFile(source, []byte(`{"private":"private-error-marker`), 0600)
	c := testCertificate(t, "example.test", []string{"example.test"}, false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var statuses []string
	err := e.Watch(ctx, time.Second, func(report Report) error {
		data, _ := json.Marshal(report)
		if bytes.Contains(data, []byte("example.test")) || bytes.Contains(data, []byte("private-error-marker")) {
			t.Fatal("watch exposed private values")
		}
		statuses = append(statuses, report.Status)
		if len(statuses) == 1 {
			writeState(t, source, c)
		} else {
			cancel()
		}
		return nil
	})
	if err != nil || !reflect.DeepEqual(statuses, []string{"retrying", "ready"}) {
		t.Fatal("watch did not recover from invalid ACME state")
	}
}

func TestExportRejectsSymlinkOutputs(t *testing.T) {
	for _, target := range []string{"certs", "private", "certs/example.test.crt", "private/example.test.key"} {
		t.Run(target, func(t *testing.T) {
			e, source, destination := fixture(t)
			writeState(t, source, testCertificate(t, "example.test", []string{"example.test"}, false))
			outside := filepath.Join(t.TempDir(), "untouched")
			os.WriteFile(outside, []byte("do-not-change"), 0600)
			if strings.Contains(target, "/") {
				os.MkdirAll(filepath.Dir(filepath.Join(destination, target)), 0700)
			}
			if err := os.Symlink(outside, filepath.Join(destination, target)); err != nil {
				t.Skip("symlinks unavailable")
			}
			if _, err := e.Export(t.Context()); err == nil {
				t.Fatal("export accepted a symlink target")
			}
			data, _ := os.ReadFile(outside)
			if string(data) != "do-not-change" {
				t.Fatal("export wrote outside destination")
			}
		})
	}
}

func TestCertificateOptionsAndInputBounds(t *testing.T) {
	e, source, _ := fixture(t)
	for _, field := range []string{"source", "destination", "domain", "resolver"} {
		options := e.options
		switch field {
		case "source":
			options.Source = "relative.json"
		case "destination":
			options.Destination = filepath.VolumeName(options.Destination) + string(os.PathSeparator)
		case "domain":
			options.Domain = "../../private-marker"
		case "resolver":
			options.Resolver = "private-marker\nresolver"
		}
		if _, err := New(options); err == nil || strings.Contains(err.Error(), "private-marker") {
			t.Fatal("invalid export options accepted or echoed")
		}
	}
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if f.Truncate(maxACMEBytes+1) != nil || f.Close() != nil {
		t.Fatal("cannot create oversized input fixture")
	}
	if _, err := e.Export(t.Context()); err == nil {
		t.Fatal("oversized ACME state accepted")
	}
	if err := e.Watch(t.Context(), 0, func(Report) error { t.Fatal("invalid watch interval touched output"); return nil }); err == nil {
		t.Fatal("zero polling interval accepted")
	}
}

func TestUnchangedExportRepairsPrivateKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions required")
	}
	e, source, destination := fixture(t)
	writeState(t, source, testCertificate(t, "example.test", []string{"example.test"}, false))
	if _, err := e.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(destination, "private/example.test.key")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unchanged export left a public private key")
	}
}
