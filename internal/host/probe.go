// Package host provides Linux prerequisite checks and owned helper services.
// Public controller wiring selects which operations are available.
package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type Observation struct {
	Facts  deployment.HostFacts
	Checks []deployment.Check
}

type probe struct {
	os, arch string
	read     func(string) ([]byte, error)
	docker   func(context.Context, ...string) ([]byte, error)
}

func Probe(ctx context.Context, ports []deployment.Port) (Observation, error) {
	return (probe{runtime.GOOS, runtime.GOARCH, readLocal, dockerLocal}).observe(ctx, ports)
}

func readLocal(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, errors.New("host observation exceeded read limit")
	}
	return data, nil
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 1<<20 {
		return 0, errors.New("Docker inventory exceeded output limit")
	}
	return b.Buffer.Write(data)
}

func dockerLocal(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
	var out limitedOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("local Docker inventory unavailable")
	}
	return out.Bytes(), nil
}

const containersFormat = `{"id":{{json .ID}},"project":{{json (.Label "com.docker.compose.project")}}}`

func (p probe) observe(ctx context.Context, ports []deployment.Port) (Observation, error) {
	o := Observation{Facts: deployment.HostFacts{OS: p.os, Architecture: p.arch, Ports: []deployment.PortObservation{}}, Checks: []deployment.Check{}}
	add := func(id string, ok bool, reason string) {
		check := deployment.Check{ID: id, OK: ok}
		if !ok {
			check.Reason = reason
		}
		o.Checks = append(o.Checks, check)
	}
	if err := ctx.Err(); err != nil {
		return o, err
	}
	platform := p.os == "linux" && (p.arch == "amd64" || p.arch == "arm64")
	add("platform", platform, "fresh deployment requires Linux amd64 or arm64")
	if !platform {
		return o, nil
	}
	release, err := p.read("/etc/os-release")
	id := distribution(release)
	add("distribution", err == nil && (id == "ubuntu" || id == "debian"), "supported Ubuntu/Debian distribution could not be established")
	if id == "ubuntu" || id == "debian" {
		o.Facts.Distribution = id
	}
	type socketTable struct {
		data []byte
		err  error
	}
	tables := map[string]socketTable{}
	readSockets := func(name string) ([]byte, error) {
		if table, ok := tables[name]; ok {
			return table.data, table.err
		}
		data, err := p.read("/proc/net/" + name)
		tables[name] = socketTable{data, err}
		return data, err
	}
	for _, port := range ports {
		observation := deployment.PortObservation{Port: port}
		if (port.Protocol == "tcp" || port.Protocol == "udp") && port.Number > 0 && port.Number <= 65535 {
			v4, err4 := readSockets(port.Protocol)
			v6, err6 := readSockets(port.Protocol + "6")
			a, valid4 := bound(v4, port)
			b, valid6 := bound(v6, port)
			observation.Known = err4 == nil && err6 == nil && valid4 && valid6
			observation.Occupied = a || b
		}
		o.Facts.Ports = append(o.Facts.Ports, observation)
		add("port-"+port.Protocol+"-"+strconv.Itoa(port.Number), observation.Known && !observation.Occupied, "port is occupied or its IPv4/IPv6 socket state could not be established")
	}
	data, err := p.docker(ctx, "ps", "--all", "--no-trunc", "--format", containersFormat)
	containers, projects, parsed := containerCounts(data)
	add("docker-inventory", err == nil && parsed, "local Docker container inventory is unavailable or malformed")
	if err != nil || !parsed {
		return o, ctx.Err()
	}
	o.Facts.Containers, o.Facts.ComposeProjects = containers, projects
	add("existing-containers", containers == 0, "existing containers or Compose projects require explicit integration; fresh deployment is blocked")
	for _, kind := range []string{"network", "volume"} {
		data, err := p.docker(ctx, kind, "ls", "--format", "{{json .Name}}")
		names, parsed := nameList(data)
		valid := err == nil && parsed
		count := 0
		for _, name := range names {
			if kind == "network" && (name == "bridge" || name == "host" || name == "none") {
				continue
			}
			count++
		}
		if valid {
			if kind == "network" {
				o.Facts.CustomNetworks = count
			} else {
				o.Facts.Volumes = count
			}
		}
		add("docker-"+kind+"-inventory", valid, "local Docker resource inventory is unavailable or malformed")
		add("existing-"+kind+"s", valid && count == 0, "existing Docker resources require explicit ownership review; fresh deployment is blocked")
	}
	data, err = p.docker(ctx, "compose", "version", "--short")
	version := strings.TrimPrefix(strings.TrimSpace(string(data)), "v")
	parts := strings.SplitN(version, ".", 3)
	composeOK := len(parts) == 3
	if composeOK {
		major, e1 := strconv.Atoi(parts[0])
		minor, e2 := strconv.Atoi(parts[1])
		patch := strings.SplitN(parts[2], "-", 2)[0]
		_, e3 := strconv.Atoi(patch)
		composeOK = e1 == nil && e2 == nil && e3 == nil && major >= 2 && minor >= 0
	}
	add("compose-plugin", err == nil && composeOK, "Docker Compose plugin is unavailable or its version could not be established")
	return o, ctx.Err()
}

func distribution(data []byte) string {
	id, count := "", 0
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && key == "ID" {
			id = strings.Trim(value, "\"'")
			count++
		}
	}
	if count != 1 {
		return ""
	}
	return id
}

func bound(data []byte, port deployment.Port) (bool, bool) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "local_address") {
		return false, false
	}
	occupied := false
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return false, false
		}
		_, local, ok := strings.Cut(fields[1], ":")
		n, err := strconv.ParseUint(local, 16, 16)
		_, stateErr := strconv.ParseUint(fields[3], 16, 8)
		if !ok || err != nil || stateErr != nil {
			return false, false
		}
		if int(n) == port.Number {
			occupied = true
		}
	}
	return occupied, true
}

func containerCounts(data []byte) (int, int, bool) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	ids, projects := map[string]bool{}, map[string]bool{}
	for {
		var row struct {
			ID      string `json:"id"`
			Project string `json:"project"`
		}
		err := d.Decode(&row)
		if err == io.EOF {
			return len(ids), len(projects), true
		}
		if err != nil || len(row.ID) != 64 || strings.Trim(row.ID, "0123456789abcdef") != "" || ids[row.ID] || len(ids) >= 10000 {
			return 0, 0, false
		}
		ids[row.ID] = true
		if row.Project != "" {
			projects[row.Project] = true
		}
	}
}

func nameList(data []byte) ([]string, bool) {
	d := json.NewDecoder(bytes.NewReader(data))
	names, seen := []string{}, map[string]bool{}
	for {
		var name string
		err := d.Decode(&name)
		if err == io.EOF {
			return names, true
		}
		if err != nil || name == "" || seen[name] || len(names) >= 10000 {
			return nil, false
		}
		seen[name] = true
		names = append(names, name)
	}
}
