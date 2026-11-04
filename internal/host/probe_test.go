package host

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

const socketHeader = "sl local_address rem_address st\n"

func fixtureProbe(t *testing.T, scenario string) probe {
	t.Helper()
	return probe{os: "linux", arch: "amd64", read: func(path string) ([]byte, error) {
		if path == "/etc/os-release" {
			if scenario == "distribution" {
				return []byte("ID=private-unsupported\n"), nil
			}
			return []byte("ID=\"ubuntu\"\nNAME=private-host-details\n"), nil
		}
		if !strings.HasPrefix(path, "/proc/net/") {
			t.Fatal("unexpected host read")
		}
		if scenario == "unknown-sockets" {
			return nil, errors.New("private-read-error")
		}
		if scenario == "malformed-sockets" {
			return []byte("private malformed table"), nil
		}
		if scenario == "busy-ipv4" && path == "/proc/net/tcp" {
			return []byte(socketHeader + "0: 00000000:01BB 00000000:0000 0A\n"), nil
		}
		if scenario == "busy-ipv6" && path == "/proc/net/tcp6" {
			return []byte(socketHeader + "0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A\n"), nil
		}
		if scenario == "busy-udp" && path == "/proc/net/udp" {
			return []byte(socketHeader + "0: 00000000:01BB 00000000:0000 07\n"), nil
		}
		return []byte(socketHeader), nil
	}, docker: func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "ps":
			if !reflect.DeepEqual(args, []string{"ps", "--all", "--no-trunc", "--format", containersFormat}) {
				t.Fatal("container inventory requested unnecessary fields")
			}
			if scenario == "docker-unavailable" {
				return nil, errors.New("private-Docker-error")
			}
			if scenario == "malformed-containers" {
				return []byte("private raw body"), nil
			}
			if scenario == "existing-container" {
				return []byte(`{"id":"` + strings.Repeat("a", 64) + `","project":"private-project"}`), nil
			}
			return nil, nil
		case "network", "volume":
			if !reflect.DeepEqual(args, []string{args[0], "ls", "--format", "{{json .Name}}"}) {
				t.Fatal("resource inventory requested unnecessary fields")
			}
			if scenario == "unavailable-"+args[0] {
				return nil, errors.New("private-resource-error")
			}
			if args[0] == "network" {
				data := "\"bridge\"\n\"host\"\n\"none\"\n"
				if scenario == "existing-network" {
					data += "\"private-network\"\n"
				}
				return []byte(data), nil
			}
			if scenario == "existing-volume" {
				return []byte("\"private-volume\"\n"), nil
			}
			return nil, nil
		case "compose":
			if !reflect.DeepEqual(args, []string{"compose", "version", "--short"}) {
				t.Fatal("preflight invoked Compose deployment")
			}
			if scenario == "compose-unavailable" {
				return nil, errors.New("private-compose-error")
			}
			return []byte("v2.38.0\n"), nil
		default:
			t.Fatal("preflight invoked an unapproved Docker command")
			return nil, nil
		}
	}}
}

func TestHostPreflightRejectsKnownAndUnknownConflicts(t *testing.T) {
	for _, scenario := range []string{"clear", "distribution", "unknown-sockets", "malformed-sockets", "busy-ipv4", "busy-ipv6", "busy-udp", "docker-unavailable", "malformed-containers", "existing-container", "existing-network", "existing-volume", "unavailable-network", "unavailable-volume", "compose-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			o, err := fixtureProbe(t, scenario).observe(context.Background(), []deployment.Port{{Protocol: "tcp", Number: 443}, {Protocol: "udp", Number: 443}})
			if err != nil {
				t.Fatal(err)
			}
			clear := true
			for _, check := range o.Checks {
				clear = clear && check.OK
			}
			if clear != (scenario == "clear") {
				t.Fatal("preflight accepted a conflict or rejected the empty host")
			}
			public, _ := json.Marshal(o)
			if strings.Contains(string(public), "private-") {
				t.Fatal("preflight exposed raw host names, labels or errors")
			}
			if scenario == "existing-container" && (o.Facts.Containers != 1 || o.Facts.ComposeProjects != 1) {
				t.Fatal("existing Compose project was not counted")
			}
		})
	}
}

func TestUnsupportedPlatformAndCancellationDoNotProbe(t *testing.T) {
	for _, platform := range []struct{ os, arch string }{{"windows", "amd64"}, {"linux", "386"}} {
		p := fixtureProbe(t, "clear")
		p.os, p.arch = platform.os, platform.arch
		p.read = func(string) ([]byte, error) { t.Fatal("unsupported platform accessed Linux state"); return nil, nil }
		p.docker = func(context.Context, ...string) ([]byte, error) {
			t.Fatal("unsupported platform invoked Docker")
			return nil, nil
		}
		o, err := p.observe(context.Background(), nil)
		if err != nil || len(o.Checks) != 1 || o.Checks[0].OK {
			t.Fatal("unsupported platform accepted")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.observe(ctx, nil); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled preflight proceeded")
		}
	}
}

func TestSocketParserPreservesProtocolAndUnknownState(t *testing.T) {
	port := deployment.Port{Protocol: "tcp", Number: 443}
	for _, data := range []string{"", socketHeader + "private malformed\n", socketHeader + "0: 00000000:ZZZZ 00000000:0000 0A\n"} {
		if _, known := bound([]byte(data), port); known {
			t.Fatal("malformed socket table treated as empty")
		}
	}
	for _, state := range []string{"0A", "01", "06"} {
		occupied, known := bound([]byte(socketHeader+"0: 00000000:01BB 00000000:0000 "+state+"\n"), port)
		if !known || !occupied {
			t.Fatal("bound TCP port treated as free")
		}
	}
	if occupied, known := bound([]byte(socketHeader+"0: 00000000:C001 00000000:01BB 01\n"), port); !known || occupied {
		t.Fatal("remote destination mistaken for a local port")
	}
}
