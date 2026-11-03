// Package certificates exports one configured Traefik ACME certificate without
// exposing account keys or changing any running service.
package certificates

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

const maxACMEBytes = 16 << 20

type Options struct {
	Source, Destination, Resolver, Domain string
}

type Report struct {
	Status       string `json:"status"`
	ChangedFiles int    `json:"changed_files"`
}

type Exporter struct{ options Options }

func New(options Options) (*Exporter, error) {
	if !filepath.IsAbs(options.Source) || !filepath.IsAbs(options.Destination) ||
		filepath.Clean(options.Destination) == filepath.VolumeName(options.Destination)+string(os.PathSeparator) ||
		!dnsName(options.Domain) || options.Resolver == "" || len(options.Resolver) > 100 ||
		strings.IndexFunc(options.Resolver, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return nil, errors.New("certificate export requires absolute paths, a resolver and a lowercase DNS basename")
	}
	return &Exporter{options: options}, nil
}

func dnsName(name string) bool {
	if len(name) > 253 || !strings.Contains(name, ".") || net.ParseIP(name) != nil {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

type certificate struct {
	Domain struct {
		Main string   `json:"main"`
		SANs []string `json:"sans"`
	} `json:"domain"`
	Certificate []byte `json:"certificate"`
	Key         []byte `json:"key"`
}

func (e *Exporter) readPair() (*certificate, error) {
	info, err := os.Lstat(e.options.Source)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxACMEBytes {
		return nil, errors.New("cannot read regular bounded ACME state")
	}
	f, err := os.Open(e.options.Source)
	if err != nil {
		return nil, errors.New("cannot open ACME state")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxACMEBytes+1))
	if err != nil || len(data) > maxACMEBytes {
		return nil, errors.New("cannot read bounded ACME state")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var resolvers map[string]json.RawMessage
	if json.Unmarshal(data, &resolvers) != nil {
		return nil, errors.New("ACME state is incomplete or invalid")
	}
	var resolver struct {
		Certificates []certificate `json:"Certificates"`
	}
	if value := resolvers[e.options.Resolver]; len(value) != 0 && json.Unmarshal(value, &resolver) != nil {
		return nil, errors.New("ACME resolver state is invalid")
	}
	var selected *certificate
	for _, candidate := range resolver.Certificates {
		if strings.TrimPrefix(candidate.Domain.Main, "*.") != e.options.Domain {
			continue
		}
		if selected != nil {
			return nil, errors.New("ACME certificate selection is ambiguous")
		}
		selected = &candidate
	}
	if selected == nil {
		return nil, nil
	}
	pair, err := tls.X509KeyPair(selected.Certificate, selected.Key)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, errors.New("ACME certificate and private key are invalid or mismatched")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("ACME leaf certificate is invalid")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("ACME leaf certificate is outside its validity period")
	}
	for _, name := range append([]string{selected.Domain.Main}, selected.Domain.SANs...) {
		probe := strings.TrimPrefix(name, "*.")
		if !dnsName(probe) {
			return nil, errors.New("ACME certificate has an invalid DNS name")
		}
		if strings.HasPrefix(name, "*.") {
			probe = "certificate-check." + probe
		}
		if leaf.VerifyHostname(probe) != nil {
			return nil, errors.New("ACME certificate does not cover its declared DNS names")
		}
	}
	return selected, nil
}

// Export validates the whole pair before touching the destination. Files are
// individually atomic, key first; existing readers can briefly observe a mixed
// pair during renewal. A subsequent export repairs an interrupted publication.
func (e *Exporter) Export(ctx context.Context) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	pair, err := e.readPair()
	if err != nil {
		return Report{}, err
	}
	if pair == nil {
		return Report{Status: "waiting"}, nil
	}
	info, err := os.Lstat(e.options.Destination)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Report{}, errors.New("certificate destination must be an existing directory without a root symlink")
	}
	unlock, err := deployment.Lock(e.options.Destination)
	if err != nil {
		return Report{}, errors.New("cannot acquire certificate export lock")
	}
	defer unlock()
	root, err := os.OpenRoot(e.options.Destination)
	if err != nil {
		return Report{}, errors.New("cannot open certificate destination")
	}
	defer root.Close()
	for _, dir := range []struct {
		name string
		mode os.FileMode
	}{{"certs", 0755}, {"private", 0700}} {
		if err := prepareDirectory(root, dir.name, dir.mode); err != nil {
			return Report{}, err
		}
	}
	targets := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"private/" + e.options.Domain + ".key", pair.Key, 0600},
		{"certs/" + e.options.Domain + ".crt", pair.Certificate, 0644},
	}
	var staged []*stagedFile
	defer func() {
		for _, file := range staged {
			file.cleanup()
		}
	}()
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		file, err := stageFile(root, target.name, target.data, target.mode)
		if err != nil {
			return Report{}, err
		}
		staged = append(staged, file)
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	report := Report{Status: "ready"}
	for _, file := range staged {
		changed, err := file.commit()
		if err != nil {
			return Report{}, err
		}
		if changed {
			report.ChangedFiles++
		}
	}
	return report, nil
}

// Watch retries empty, rewritten or invalid ACME state without erasing the last
// exported pair. Diagnostics contain only status and changed-file counts.
func (e *Exporter) Watch(ctx context.Context, interval time.Duration, emit func(Report) error) error {
	if interval < time.Second || interval > 24*time.Hour {
		return errors.New("certificate polling interval must be between one second and 24 hours")
	}
	var previous string
	for {
		if ctx.Err() != nil {
			return nil
		}
		report, err := e.Export(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			report = Report{Status: "retrying"}
		}
		if report.Status != previous || report.ChangedFiles > 0 {
			if err := emit(report); err != nil {
				return errors.New("cannot report certificate export status")
			}
			previous = report.Status
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
