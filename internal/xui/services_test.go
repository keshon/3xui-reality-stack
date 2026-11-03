package xui

import (
	"bytes"
	"strings"
	"testing"
)

func TestCertificateServiceBindsRootAndExplicitController(t *testing.T) {
	file, err := certificateService("/opt/my stack", "/opt/stackctl-go/v0.1.0-go.1/stackctl-go", "www.example.test")
	if err != nil {
		t.Fatal(err)
	}
	unit := string(file.Data)
	for _, expected := range []string{
		`ExecStart=:"/opt/stackctl-go/v0.1.0-go.1/stackctl-go" "export-certificates"`,
		`"--source" "/opt/my stack/traefik/data/acme/acme.json"`,
		`"--destination" "/opt/my stack/shared/certs"`,
		`"--domain" "www.example.test" "--resolver" "le" "--watch"`,
		`ReadWritePaths="/opt/my stack/shared/certs"`,
	} {
		if !strings.Contains(unit, expected) {
			t.Fatal("certificate service lost explicit binding")
		}
	}
	again, _ := certificateService("/opt/my stack", "/opt/stackctl-go/v0.1.0-go.1/stackctl-go", "www.example.test")
	other, _ := certificateService("/opt/other", "/opt/stackctl-go/v0.1.0-go.1/stackctl-go", "www.example.test")
	if file.Name != again.Name || !bytes.Equal(file.Data, again.Data) || file.Name == other.Name {
		t.Fatal("service identity does not follow installation root")
	}
	for _, root := range []string{"/", "/opt/../tmp", "relative", "/opt/private-marker\n"} {
		if _, err := certificateService(root, "/opt/bin/controller", "www.example.test"); err == nil || strings.Contains(err.Error(), "private-marker") {
			t.Fatal("invalid root accepted or echoed")
		}
	}
}
