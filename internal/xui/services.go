package xui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"strings"

	"github.com/keshon/3xui-reality-stack/internal/host"
)

type serviceFile struct {
	Name string
	Data []byte
}

// certificateService binds an eventual unit to an explicit installed release
// binary and installation root. It does not discover a binary through PATH or
// create/enable a service. Fresh-install orchestration will consume this file.
func certificateService(root, controller, domain string) (serviceFile, error) {
	if !path.IsAbs(root) || path.Clean(root) != root || root == "/" || !initialDomain(domain) ||
		strings.ContainsAny(root, "\r\n\x00") {
		return serviceFile{}, errors.New("certificate service requires a canonical Linux installation root and certificate basename")
	}
	data, err := host.RenderService(host.SupervisedCommand{
		Executable:     controller,
		Arguments:      []string{"export-certificates", "--source", root + "/traefik/data/acme/acme.json", "--destination", root + "/shared/certs", "--domain", domain, "--resolver", "le", "--watch"},
		WriteDirectory: root + "/shared/certs",
	})
	if err != nil {
		return serviceFile{}, err
	}
	sum := sha256.Sum256([]byte(root))
	return serviceFile{Name: "stackctl-certs-" + hex.EncodeToString(sum[:8]) + ".service", Data: data}, nil
}
