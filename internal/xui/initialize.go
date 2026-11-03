package xui

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	stackassets "github.com/keshon/3xui-reality-stack"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type initialInput struct {
	Schema        int      `json:"schema_version"`
	BaseDomain    string   `json:"base_domain"`
	PublicDomain  string   `json:"public_domain"`
	PanelDomain   string   `json:"panel_domain"`
	Email         string   `json:"acme_email"`
	ACMEMode      string   `json:"acme_mode"`
	DNSToken      string   `json:"dns_token"`
	PanelUser     string   `json:"panel_user"`
	PanelPassword string   `json:"panel_password"`
	Users         []string `json:"users"`
}

type initialization struct {
	Version   int               `json:"version"`
	InputHash string            `json:"input_hash"`
	Files     map[string]string `json:"files"`
}

var accountName = regexp.MustCompile(`^[a-z0-9_]{1,24}$`)
var initializationFiles = append(append([]string{".stack/config.json", ".env", "users.txt", "nodes.txt", "compatibility.json"}, renderedFiles...), acmeStateFile)

func initialDomain(value string) bool {
	if !panelDomain(value) {
		return false
	}
	suffix := value[strings.LastIndexByte(value, '.')+1:]
	if len(suffix) < 2 {
		return false
	}
	for _, r := range suffix {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func initialConfiguration(raw json.RawMessage, entropy io.Reader) (config, string, error) {
	var input initialInput
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 32<<10 || !utf8.Valid(raw) || !json.Valid(raw) || d.Decode(&input) != nil || input.Schema != 1 {
		return config{}, "", errors.New("invalid initialization input schema")
	}
	canonicalDomain := func(value string) string { return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), ".")) }
	input.BaseDomain = canonicalDomain(input.BaseDomain)
	input.PublicDomain = canonicalDomain(input.PublicDomain)
	input.PanelDomain = canonicalDomain(input.PanelDomain)
	if !initialDomain(input.BaseDomain) || !initialDomain(input.PublicDomain) || !initialDomain(input.PanelDomain) || input.PublicDomain == input.PanelDomain {
		return config{}, "", errors.New("distinct public and panel DNS hostnames and a base domain are required")
	}
	if input.ACMEMode == "" {
		input.ACMEMode = "http"
	}
	if input.ACMEMode != "http" && input.ACMEMode != "dns" {
		return config{}, "", errors.New("ACME mode must be http or dns")
	}
	if input.ACMEMode == "dns" {
		if input.DNSToken == "" {
			return config{}, "", errors.New("DNS-01 requires a DNS API token")
		}
		for _, name := range []string{input.PublicDomain, input.PanelDomain} {
			if !strings.HasSuffix(name, "."+input.BaseDomain) || strings.Count(name, ".") != strings.Count(input.BaseDomain, ".")+1 {
				return config{}, "", errors.New("DNS-01 hostnames must be directly below the base domain")
			}
		}
	} else if input.DNSToken != "" {
		return config{}, "", errors.New("HTTP-01 input must not include a DNS API token")
	}
	address, err := mail.ParseAddress(input.Email)
	_, emailDomain, _ := strings.Cut(input.Email, "@")
	if err != nil || address.Address != input.Email || strings.Count(input.Email, "@") != 1 || strings.IndexFunc(input.Email, unicode.IsSpace) >= 0 || !initialDomain(strings.ToLower(emailDomain)) {
		return config{}, "", errors.New("a plain ACME email address is required")
	}
	if len(input.Users) == 0 || len(input.Users) > 1000 {
		return config{}, "", errors.New("initialize with between one and 1000 unique account names")
	}
	seen := map[string]bool{}
	for _, user := range input.Users {
		if !accountName.MatchString(user) || seen[user] {
			return config{}, "", errors.New("account names must be unique lowercase letters, digits or underscores, up to 24 characters")
		}
		seen[user] = true
	}
	for _, value := range []string{input.Email, input.DNSToken, input.PanelUser, input.PanelPassword} {
		if controls(value) || len(value) > 4096 {
			return config{}, "", errors.New("initial credentials must be literal UTF-8 without controls, within 4096 bytes")
		}
	}
	if strings.IndexFunc(input.PanelUser, unicode.IsSpace) >= 0 {
		return config{}, "", errors.New("panel username must not contain whitespace")
	}
	canonical, _ := json.Marshal(input)
	sum := sha256.Sum256(canonical)
	inputHash := hex.EncodeToString(sum[:])
	secret := func(size int) (string, error) {
		data := make([]byte, size)
		if _, err := io.ReadFull(entropy, data); err != nil {
			return "", errors.New("cannot generate private initialization values")
		}
		return hex.EncodeToString(data), nil
	}
	paths := map[string]string{}
	for _, field := range []struct{ key, prefix string }{{"PANEL_PATH", "panel"}, {"SUB_PATH", "sub"}, {"JSON_PATH", "json"}} {
		value, err := secret(6)
		if err != nil {
			return config{}, "", err
		}
		paths[field.key] = "/" + field.prefix + "-" + value
	}
	if input.PanelUser == "" {
		value, err := secret(3)
		if err != nil {
			return config{}, "", err
		}
		input.PanelUser = "adm" + value
	}
	if input.PanelPassword == "" {
		data := make([]byte, 24)
		if _, err := io.ReadFull(entropy, data); err != nil {
			return config{}, "", errors.New("cannot generate private initialization values")
		}
		input.PanelPassword = base64.RawURLEncoding.EncodeToString(data)
	}
	cert := input.PublicDomain
	if input.ACMEMode == "dns" {
		cert = input.BaseDomain
	}
	env := map[string]string{
		"ROLE": "standalone", "TZ": "Etc/UTC", "BASE_DOMAIN": input.BaseDomain, "PUBLIC_DOMAIN": input.PublicDomain, "PANEL_DOMAIN": input.PanelDomain,
		"PANEL_USER": input.PanelUser, "PANEL_PASS": input.PanelPassword, "PANEL_PATH": paths["PANEL_PATH"], "SUB_PATH": paths["SUB_PATH"], "JSON_PATH": paths["JSON_PATH"], "TROJAN_PATH": "/assets/stream",
		"ACME_MODE": input.ACMEMode, "LETSENCRYPT_EMAIL": input.Email, "CF_DNS_API_TOKEN": input.DNSToken, "CERT_BASENAME": cert,
		"MANAGEMENT_BIND": "127.0.0.1", "MANAGEMENT_PORT": "2053", "PROXY_SUBNET": "172.28.0.0/16",
		"REALITY_FINGERPRINT": "chrome", "RU_POLICY": "allow", "LANES": "vless-reality,trojan-httpupgrade,hysteria2",
		"MTPROTO_PORT": "8443", "AWG_PORT": "28844", "AWG_SUBNET": "10.8.1.0", "AWG_MTU": "1280",
		"USERS": strings.Join(input.Users, ","), "USERS_FILE": "users.txt", "NODES_FILE": "nodes.txt", "USER_LIMIT_GB": "0", "USER_EXPIRY_DAYS": "0", "BACKUP_KEEP": "7", "BACKUP_DIR": "backups",
	}
	return config{Schema: 1, Env: env, Users: input.Users, Nodes: []map[string]string{}}, inputHash, nil
}

func initialArtifacts(c config) (map[string][]byte, error) {
	if _, err := newPanelBootstrap(c); err != nil {
		return nil, err
	}
	artifacts, err := renderDeployment(c)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, errors.New("cannot render initial configuration")
	}
	keys := make([]string, 0, len(c.Env))
	for key := range c.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var env strings.Builder
	env.WriteString("# Generated by the Go controller. Desired configuration lives in .stack/config.json.\n")
	for _, key := range keys {
		quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(c.Env[key])
		env.WriteString(key + `="` + quoted + "\"\n")
	}
	artifacts[".stack/config.json"] = append(data, '\n')
	artifacts[".env"] = []byte(env.String())
	artifacts["users.txt"] = []byte(strings.Join(c.Users, "\n") + "\n")
	artifacts["nodes.txt"] = []byte{}
	artifacts["compatibility.json"] = stackassets.Compatibility()
	artifacts[acmeStateFile] = []byte{}
	return artifacts, nil
}

func initializationReport() deployment.InitializationReport {
	return deployment.InitializationReport{Stack: stackID, Status: "prepared", Files: append([]string(nil), initializationFiles...), CanApply: false, Observation: "private standalone configuration and deployment files only; bundled decoy is an example to replace; no applied baseline, certificate export service, certificates or services created; fresh-install execution is not implemented", Stages: []deployment.Action{
		{ID: "host", Resource: "host", Effects: []string{"inspect and review host resources and installation requirements"}},
		{ID: "runtime", Resource: "docker", Effects: []string{"prepare reviewed Docker and network resources"}},
		{ID: "services", Resource: "stack", Effects: []string{"validate deployment files, obtain certificates, configure panel, transports and accounts"}},
		{ID: "verify", Resource: "stack", Effects: []string{"verify subscriptions and authenticated transfers before committing applied state"}},
	}}
}

func (Stack) Initialize(ctx context.Context, root string, raw json.RawMessage) (deployment.InitializationReport, error) {
	if err := ctx.Err(); err != nil {
		return deployment.InitializationReport{}, err
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) == filepath.VolumeName(root)+string(os.PathSeparator) {
		return deployment.InitializationReport{}, errors.New("initialization requires an absolute new installation directory")
	}
	root = filepath.Clean(root)
	c, inputHash, err := initialConfiguration(raw, rand.Reader)
	if err != nil {
		return deployment.InitializationReport{}, err
	}
	if _, err := os.Lstat(root); err == nil {
		return reopenInitialization(ctx, root, inputHash)
	} else if !os.IsNotExist(err) {
		return deployment.InitializationReport{}, errors.New("cannot inspect initialization target")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		return deployment.InitializationReport{}, errors.New("initialization parent directory must already exist")
	}
	root = filepath.Join(parent, filepath.Base(root))
	stage, err := os.MkdirTemp(parent, ".stack-initialize-")
	if err != nil {
		return deployment.InitializationReport{}, errors.New("cannot create private initialization staging directory")
	}
	defer func() {
		if stage != "" {
			for _, name := range append(append([]string(nil), initializationFiles...), ".stack/go-initialization.json", ".stack/lock") {
				_ = os.Remove(filepath.Join(stage, name))
			}
			directories := append(append([]string(nil), renderedDirectories...), runtimeDirectories...)
			for i := len(directories) - 1; i >= 0; i-- {
				_ = os.Remove(filepath.Join(stage, directories[i]))
			}
			_ = os.Remove(filepath.Join(stage, ".stack"))
			_ = os.Remove(stage)
		}
	}()
	unlock, err := deployment.Lock(stage)
	if err != nil {
		return deployment.InitializationReport{}, err
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	artifacts, err := initialArtifacts(c)
	if err != nil {
		return deployment.InitializationReport{}, err
	}
	if err := prepareDraftDirectories(stage); err != nil {
		return deployment.InitializationReport{}, err
	}
	state := initialization{Version: 1, InputHash: inputHash, Files: map[string]string{}}
	for _, name := range initializationFiles {
		if err := ctx.Err(); err != nil {
			return deployment.InitializationReport{}, err
		}
		data := artifacts[name]
		if err := deployment.WriteFileMode(filepath.Join(stage, name), data, artifactMode(name)); err != nil {
			return deployment.InitializationReport{}, err
		}
		sum := sha256.Sum256(data)
		state.Files[name] = hex.EncodeToString(sum[:])
	}
	if err := deployment.WriteJSON(filepath.Join(stage, ".stack/go-initialization.json"), state); err != nil {
		return deployment.InitializationReport{}, err
	}
	// File writes sync their own parents. Also persist empty bind directories
	// and their ancestors before making the complete installation root visible.
	directories := append(append([]string(nil), renderedDirectories...), runtimeDirectories...)
	for i := len(directories) - 1; i >= 0; i-- {
		if err := deployment.SyncDirectory(filepath.Join(stage, directories[i])); err != nil {
			return deployment.InitializationReport{}, errors.New("cannot synchronize draft deployment directories")
		}
	}
	if err := deployment.SyncDirectory(stage); err != nil {
		return deployment.InitializationReport{}, errors.New("cannot synchronize initialization stage")
	}
	if err := ctx.Err(); err != nil {
		return deployment.InitializationReport{}, err
	}
	// No controller can address the private staging path. Close its lock before
	// publication so Windows can rename the directory as well as Linux.
	unlock()
	unlock = nil
	if err := deployment.PublishDirectory(stage, root); err != nil {
		return deployment.InitializationReport{}, err
	}
	stage = ""
	return initializationReport(), nil
}

func reopenInitialization(ctx context.Context, root, inputHash string) (deployment.InitializationReport, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return deployment.InitializationReport{}, errors.New("existing initialization target is not a private draft directory")
	}
	if info, err := os.Lstat(filepath.Join(root, ".stack/go-initialization.json")); err != nil || !info.Mode().IsRegular() {
		return deployment.InitializationReport{}, errors.New("existing installations cannot be initialized; use inspection and adoption")
	}
	unlock, err := deployment.Lock(root)
	if err != nil {
		return deployment.InitializationReport{}, err
	}
	defer unlock()
	if err := (deployment.Engine{Root: root}).CheckIdle(); err != nil {
		return deployment.InitializationReport{}, err
	}
	state, _, err := readDraft(root)
	if err != nil || state.InputHash != inputHash {
		return deployment.InitializationReport{}, errors.New("initialization inputs or draft metadata differ; preserve the existing draft")
	}
	if err := ctx.Err(); err != nil {
		return deployment.InitializationReport{}, err
	}
	return initializationReport(), nil
}
