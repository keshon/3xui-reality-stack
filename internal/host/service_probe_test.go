package host

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const absentService = "LoadState=not-found\nActiveState=inactive\nFragmentPath=\nDropInPaths=\nUnitFileState=\n"

func TestServiceObservationRequiresCompleteAbsentState(t *testing.T) {
	for _, scenario := range []string{"absent", "loaded", "active", "fragment", "drop-in", "enabled", "duplicate", "missing", "unexpected", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			data := absentService
			switch scenario {
			case "loaded":
				data = strings.Replace(data, "LoadState=not-found", "LoadState=loaded", 1)
			case "active":
				data = strings.Replace(data, "ActiveState=inactive", "ActiveState=active", 1)
			case "fragment":
				data = strings.Replace(data, "FragmentPath=", "FragmentPath=/etc/systemd/system/existing.service", 1)
			case "drop-in":
				data = strings.Replace(data, "DropInPaths=", "DropInPaths=/etc/systemd/system/service.d/custom.conf", 1)
			case "enabled":
				data = strings.Replace(data, "UnitFileState=", "UnitFileState=enabled", 1)
			case "duplicate":
				data += "LoadState=not-found\n"
			case "missing":
				data = strings.Replace(data, "ActiveState=inactive\n", "", 1)
			case "unexpected":
				data += "Environment=private-marker\n"
			}
			observation, err := observeService(t.Context(), "stackctl-certs-test.service", func(_ context.Context, args ...string) ([]byte, error) {
				if strings.Join(args, " ") != "--system --no-pager show --property=LoadState,ActiveState,FragmentPath,DropInPaths,UnitFileState -- stackctl-certs-test.service" {
					t.Fatal("service probe requested unrelated metadata or effects")
				}
				if scenario == "unavailable" {
					return nil, errors.New("private-marker")
				}
				return []byte(data), nil
			})
			valid := scenario != "duplicate" && scenario != "missing" && scenario != "unexpected" && scenario != "unavailable"
			if (err == nil) != valid || err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("service observation accepted malformed metadata or echoed contents")
			}
			if err == nil && observation.Absent() != (scenario == "absent") {
				t.Fatal("existing service was treated as absent")
			}
		})
	}
	if _, err := observeService(t.Context(), "bad;private-marker.service", func(context.Context, ...string) ([]byte, error) {
		t.Fatal("invalid unit reached command runner")
		return nil, nil
	}); err == nil {
		t.Fatal("invalid service name accepted")
	}
}
