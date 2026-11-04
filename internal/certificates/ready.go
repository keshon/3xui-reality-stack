package certificates

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"runtime"
)

// Ready is read-only. It requires the exported files to match a currently valid
// ACME pair covering every required host. It does not verify public CA trust;
// installation must also probe the HTTPS listener with normal TLS verification.
func (e *Exporter) Ready(ctx context.Context, hosts []string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(hosts) == 0 {
		return false, errors.New("certificate readiness requires hostnames")
	}
	pair, err := e.readPair()
	if err != nil {
		return false, err
	}
	if pair == nil {
		return false, nil
	}
	parsed, err := tlsPairLeaf(pair.Certificate, pair.Key)
	if err != nil {
		return false, err
	}
	for _, name := range hosts {
		if !dnsName(name) || parsed.VerifyHostname(name) != nil {
			return false, errors.New("certificate does not cover every required hostname")
		}
	}
	info, err := os.Lstat(e.options.Destination)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("certificate readiness destination is invalid")
	}
	root, err := os.OpenRoot(e.options.Destination)
	if err != nil {
		return false, errors.New("cannot inspect certificate readiness")
	}
	defer root.Close()
	for _, dir := range []string{"certs", "private"} {
		info, err := root.Lstat(dir)
		if os.IsNotExist(err) {
			return false, nil
		}
		mode := os.FileMode(0755)
		if dir == "private" {
			mode = 0700
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm() != mode {
			return false, errors.New("invalid exported certificate directory")
		}
	}
	for _, file := range []struct {
		name     string
		expected []byte
		mode     os.FileMode
	}{
		{"certs/" + e.options.Domain + ".crt", pair.Certificate, 0644}, {"private/" + e.options.Domain + ".key", pair.Key, 0600},
	} {
		info, err := root.Lstat(file.name)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxACMEBytes || runtime.GOOS != "windows" && info.Mode().Perm() != file.mode {
			return false, errors.New("invalid exported certificate file")
		}
		f, err := root.Open(file.name)
		if err != nil {
			return false, errors.New("cannot read exported certificate")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxACMEBytes+1))
		f.Close()
		if readErr != nil || len(data) > maxACMEBytes {
			return false, errors.New("cannot read bounded exported certificate")
		}
		if !bytes.Equal(data, file.expected) {
			return false, nil
		}
	}
	after, err := e.readPair()
	if err != nil {
		return false, err
	}
	if after == nil || !bytes.Equal(after.Certificate, pair.Certificate) || !bytes.Equal(after.Key, pair.Key) {
		return false, nil
	}
	return true, ctx.Err()
}

func tlsPairLeaf(cert, key []byte) (*x509.Certificate, error) {
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, errors.New("invalid readiness certificate pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("invalid readiness leaf certificate")
	}
	return leaf, nil
}
