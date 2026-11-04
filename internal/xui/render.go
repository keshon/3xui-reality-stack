package xui

import (
	"encoding/json"
	"errors"
	"strings"

	stackassets "github.com/keshon/3xui-reality-stack"
)

// Compose accepts JSON as well as YAML. These documents contain literal values,
// not shell fragments or environment-file substitutions. Dollar signs still
// require Compose escaping, even inside JSON strings.
type composeDocument struct {
	Name     string                    `json:"name"`
	Networks map[string]any            `json:"networks"`
	Services map[string]composeService `json:"services"`
}

type composeService struct {
	Image         string            `json:"image"`
	ContainerName string            `json:"container_name"`
	Restart       string            `json:"restart"`
	Networks      []string          `json:"networks"`
	Ports         []string          `json:"ports,omitempty"`
	Volumes       []string          `json:"volumes"`
	Environment   map[string]string `json:"environment,omitempty"`
	Command       []string          `json:"command,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Logging       map[string]any    `json:"logging"`
}

var renderedFiles = []string{
	"xui/compose.json", "traefik/compose.json", "decoy/compose.json",
	"traefik/dynamic/tls.yml", "decoy/nginx.conf",
	"decoy/site/index.html", "decoy/site/about.html", "decoy/site/style.css",
	"decoy/site/robots.txt", "decoy/site/favicon.svg",
}

// Fixed directories are created only inside the unpublished initialization
// stage. Reverse order is also the safe order for removing empty directories.
var renderedDirectories = []string{"xui", "traefik", "traefik/dynamic", "decoy", "decoy/site"}

func renderDeployment(c config) (map[string][]byte, error) {
	e := c.Env
	if !valid(c) || e["ROLE"] != "standalone" || len(c.Nodes) != 0 ||
		e["LANES"] != "vless-reality,trojan-httpupgrade,hysteria2" ||
		e["MANAGEMENT_BIND"] != "127.0.0.1" || e["MANAGEMENT_PORT"] != "2053" ||
		e["PROXY_SUBNET"] != "172.28.0.0/16" || e["TZ"] != "Etc/UTC" {
		return nil, errors.New("deployment rendering supports only fresh standalone core-transport defaults")
	}
	for _, key := range []string{"BASE_DOMAIN", "PUBLIC_DOMAIN", "PANEL_DOMAIN", "CERT_BASENAME"} {
		if !initialDomain(e[key]) {
			return nil, errors.New("invalid deployment hostname")
		}
	}
	for _, key := range []string{"PANEL_PATH", "SUB_PATH", "JSON_PATH", "TROJAN_PATH"} {
		if !panelPath(e[key]) {
			return nil, errors.New("invalid deployment route path")
		}
	}
	if e["PUBLIC_DOMAIN"] == e["PANEL_DOMAIN"] || (e["ACME_MODE"] != "http" && e["ACME_MODE"] != "dns") {
		return nil, errors.New("invalid deployment domain or certificate mode")
	}
	var policy struct {
		Schema  int    `json:"schema"`
		Panel   string `json:"panel_image"`
		Traefik string `json:"traefik_image"`
	}
	if json.Unmarshal(stackassets.Compatibility(), &policy) != nil || policy.Schema != 1 || policy.Panel == "" || policy.Traefik == "" {
		return nil, errors.New("embedded compatibility policy is invalid")
	}
	literal := func(value string) string { return strings.ReplaceAll(value, "$", "$$") }
	service := func(name, image string) composeService {
		return composeService{Image: image, ContainerName: name, Restart: "unless-stopped", Networks: []string{"proxy"},
			Logging: map[string]any{"driver": "json-file", "options": map[string]string{"max-size": "10m", "max-file": "3"}}}
	}
	panel := service("3x-ui", policy.Panel)
	panel.Ports = []string{"443:443/tcp", "443:443/udp"}
	panel.Volumes = []string{"./db:/etc/x-ui/", "../shared/certs:/certs:ro"}
	panel.Environment = map[string]string{"TZ": e["TZ"], "XRAY_VMESS_AEAD_FORCED": "false", "XUI_ENABLE_FAIL2BAN": "true"}
	panel.Labels = map[string]string{"traefik.enable": "true"}
	router := func(labels map[string]string, name, rule, target, port string) {
		prefix := "traefik.http.routers." + name
		labels[prefix+".entrypoints"] = "websecure"
		labels[prefix+".rule"] = literal(rule)
		labels[prefix+".service"] = target
		labels["traefik.http.services."+target+".loadbalancer.server.port"] = port
	}
	router(panel.Labels, "xui-panel", "Host(`"+e["PANEL_DOMAIN"]+"`) && PathPrefix(`"+e["PANEL_PATH"]+"`)", "xui-panel-svc", "2053")
	router(panel.Labels, "xui-sub", "Host(`"+e["PUBLIC_DOMAIN"]+"`) && (PathPrefix(`"+e["SUB_PATH"]+"`) || PathPrefix(`"+e["JSON_PATH"]+"`))", "xui-sub-svc", "2096")
	router(panel.Labels, "trojan-hu", "Host(`"+e["PUBLIC_DOMAIN"]+"`) && PathPrefix(`"+e["TROJAN_PATH"]+"`)", "trojan-hu-svc", "12812")
	panel.Labels["traefik.http.routers.xui-sub.priority"] = "100"
	panel.Labels["traefik.http.routers.trojan-hu.priority"] = "100"

	traefik := service("traefik", policy.Traefik)
	traefik.Ports = []string{"80:80/tcp", "127.0.0.1:2053:8443/tcp"}
	traefik.Volumes = []string{"/var/run/docker.sock:/var/run/docker.sock:ro", "./data/logs:/logs", "./data/acme:/acme", "./dynamic:/dynamic:ro", "../shared/certs:/certs:ro"}
	traefik.Environment = map[string]string{"TZ": e["TZ"]}
	traefik.Command = []string{
		"--global.checknewversion=false", "--global.sendanonymoususage=false",
		"--entrypoints.web.address=:80", "--entrypoints.web.http.redirections.entrypoint.to=:443", "--entrypoints.web.http.redirections.entrypoint.scheme=https",
		"--entrypoints.websecure.address=:8443", "--entrypoints.websecure.http.tls=true", "--entrypoints.websecure.http.tls.certresolver=le",
		"--entrypoints.websecure.proxyProtocol.trustedIPs=" + e["PROXY_SUBNET"], "--entrypoints.websecure.forwardedHeaders.trustedIPs=" + e["PROXY_SUBNET"],
		"--entrypoints.websecure.transport.respondingTimeouts.readTimeout=30s", "--entrypoints.websecure.transport.respondingTimeouts.writeTimeout=0",
		"--entrypoints.websecure.transport.respondingTimeouts.idleTimeout=180s", "--entrypoints.websecure.transport.lifeCycle.graceTimeOut=30s",
		"--providers.docker=true", "--providers.docker.endpoint=unix:///var/run/docker.sock", "--providers.docker.network=proxy", "--providers.docker.exposedByDefault=false",
		"--providers.file.directory=/dynamic", "--providers.file.watch=true",
		"--certificatesresolvers.le.acme.email=" + literal(e["LETSENCRYPT_EMAIL"]), "--certificatesresolvers.le.acme.storage=/acme/acme.json",
		"--log.level=INFO", "--log.filepath=/logs/traefik.log", "--accesslog=true", "--accesslog.filepath=/logs/access.log", "--accesslog.bufferingsize=100",
	}
	if e["ACME_MODE"] == "dns" {
		if e["CF_DNS_API_TOKEN"] == "" || controls(e["CF_DNS_API_TOKEN"]) {
			return nil, errors.New("DNS deployment requires a literal API token")
		}
		traefik.Environment["CF_DNS_API_TOKEN"] = literal(e["CF_DNS_API_TOKEN"])
		traefik.Command = append(traefik.Command,
			"--entrypoints.websecure.http.tls.domains[0].main="+e["BASE_DOMAIN"], "--entrypoints.websecure.http.tls.domains[0].sans=*."+e["BASE_DOMAIN"],
			"--certificatesresolvers.le.acme.dnschallenge=true", "--certificatesresolvers.le.acme.dnschallenge.provider=cloudflare",
			"--certificatesresolvers.le.acme.dnschallenge.resolvers=1.1.1.1:53,8.8.8.8:53")
	} else {
		traefik.Command = append(traefik.Command,
			"--entrypoints.websecure.http.tls.domains[0].main="+e["PUBLIC_DOMAIN"],
			"--entrypoints.websecure.http.tls.domains[0].sans="+e["PANEL_DOMAIN"],
			"--certificatesresolvers.le.acme.httpchallenge=true", "--certificatesresolvers.le.acme.httpchallenge.entrypoint=web")
	}
	decoy := service("decoy", "nginx:1.29-alpine")
	decoy.Volumes = []string{"./site:/usr/share/nginx/html:ro", "./nginx.conf:/etc/nginx/conf.d/default.conf:ro"}
	decoy.Labels = map[string]string{"traefik.enable": "true"}
	router(decoy.Labels, "decoy", "Host(`"+e["PUBLIC_DOMAIN"]+"`)", "decoy-svc", "80")
	router(decoy.Labels, "decoy-catchall", "PathPrefix(`/`)", "decoy-svc", "80")
	decoy.Labels["traefik.http.routers.decoy.priority"] = "1"
	decoy.Labels["traefik.http.routers.decoy-catchall.priority"] = "0"

	files := map[string][]byte{}
	for _, project := range []struct {
		name, service string
		value         composeService
	}{{"xui", "3x-ui", panel}, {"traefik", "traefik", traefik}, {"decoy", "decoy", decoy}} {
		doc := composeDocument{Name: project.name, Networks: map[string]any{"proxy": map[string]any{"external": true, "name": "proxy"}}, Services: map[string]composeService{project.service: project.value}}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, errors.New("cannot encode deployment document")
		}
		files[project.name+"/compose.json"] = append(data, '\n')
	}
	tls := map[string]any{"tls": map[string]any{
		"stores":  map[string]any{"default": map[string]any{"defaultCertificate": map[string]string{"certFile": "/certs/certs/" + e["CERT_BASENAME"] + ".crt", "keyFile": "/certs/private/" + e["CERT_BASENAME"] + ".key"}}},
		"options": map[string]any{"default": map[string]any{"minVersion": "VersionTLS12", "sniStrict": false}},
	}}
	data, _ := json.MarshalIndent(tls, "", "  ")
	files["traefik/dynamic/tls.yml"] = append(data, '\n')
	for _, name := range renderedFiles[4:] {
		source := strings.Replace(name, "decoy/site/", "decoy/site.example/", 1)
		data, err := stackassets.Decoy(source)
		if err != nil {
			return nil, errors.New("bundled decoy asset is missing")
		}
		files[name] = data
	}
	return files, nil
}
