package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func serviceInstallFixture(t *testing.T) (*ServiceInstaller, *ServiceObservation, *[]string) {
	t.Helper()
	state := &ServiceObservation{LoadState: "not-found", ActiveState: "inactive"}
	commands := &[]string{}
	s := &ServiceInstaller{directory: t.TempDir(), observe: func(context.Context, string) (ServiceObservation, error) { return *state, nil }}
	s.run = func(_ context.Context, args ...string) error {
		*commands = append(*commands, strings.Join(args, " "))
		switch args[0] {
		case "daemon-reload":
			*state = ServiceObservation{LoadState: "loaded", ActiveState: "inactive", Fragment: filepath.Join(s.directory, "stackctl-test.service"), FileState: "disabled"}
		case "enable":
			state.ActiveState = "active"
			state.FileState = "enabled"
		default:
			t.Fatal("unexpected service mutation")
		}
		return nil
	}
	return s, state, commands
}

func TestOwnedServiceInstallIsIdempotentAndNeverRestartsAnActiveUnit(t *testing.T) {
	s, _, commands := serviceInstallFixture(t)
	owner := strings.Repeat("a", 32)
	unit := []byte("[Service]\nExecStart=/fixture/controller\n")
	if err := s.Ensure(t.Context(), "stackctl-test.service", unit, owner); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*commands, []string{"daemon-reload", "enable --now -- stackctl-test.service"}) {
		t.Fatal("unexpected unit activation sequence")
	}
	if err := s.Ensure(t.Context(), "stackctl-test.service", unit, owner); err != nil {
		t.Fatal(err)
	}
	if len(*commands) != 2 {
		t.Fatal("idempotent install restarted or reloaded an active unit")
	}
	data, err := os.ReadFile(filepath.Join(s.directory, "stackctl-test.service"))
	if err != nil || string(data) != "# stackctl-operation "+owner+"\n"+string(unit) {
		t.Fatal("unit lost reviewed bytes or ownership marker")
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil || len(entries) != 1 {
		t.Fatal("service installation left staging files")
	}
}

func TestServiceInstallRecoversPublicationAndAmbiguousEnable(t *testing.T) {
	for _, fault := range []string{"after-publication", "enable-reply"} {
		t.Run(fault, func(t *testing.T) {
			s, _, commands := serviceInstallFixture(t)
			original := s.run
			failed := false
			s.run = func(ctx context.Context, args ...string) error {
				if !failed && fault == "after-publication" && args[0] == "daemon-reload" {
					failed = true
					return errors.New("synthetic interrupted reply")
				}
				err := original(ctx, args...)
				if !failed && fault == "enable-reply" && args[0] == "enable" {
					failed = true
					return errors.New("synthetic interrupted reply")
				}
				return err
			}
			unit := []byte("[Service]\nExecStart=/fixture/controller\n")
			owner := strings.Repeat("b", 32)
			err := s.Ensure(t.Context(), "stackctl-test.service", unit, owner)
			if fault == "after-publication" && err == nil {
				t.Fatal("reload failure was ignored")
			}
			if fault == "enable-reply" && err != nil {
				t.Fatal("successful ambiguous enable was not recognized")
			}
			if err := s.Ensure(t.Context(), "stackctl-test.service", unit, owner); err != nil {
				t.Fatal(err)
			}
			if len(*commands) != 2 {
				t.Fatal("recovery duplicated activation")
			}
		})
	}
}

func TestServiceInstallRefusesForeignFilesUnitsAndDropIns(t *testing.T) {
	for _, scenario := range []string{"foreign-file", "different-operation", "foreign-loaded", "drop-ins", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			s, state, commands := serviceInstallFixture(t)
			unit := []byte("[Service]\nExecStart=/fixture/controller\n")
			owner := strings.Repeat("c", 32)
			target := filepath.Join(s.directory, "stackctl-test.service")
			switch scenario {
			case "foreign-file":
				os.WriteFile(target, unit, 0644)
			case "different-operation":
				os.WriteFile(target, append([]byte("# stackctl-operation "+strings.Repeat("d", 32)+"\n"), unit...), 0644)
			case "foreign-loaded":
				state.LoadState = "loaded"
				state.Fragment = "/usr/lib/systemd/system/existing.service"
			case "drop-ins":
				state.DropIns = "/etc/systemd/system/service.d/custom.conf"
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside")
				os.WriteFile(outside, []byte("keep"), 0600)
				if err := os.Symlink(outside, target); err != nil {
					t.Skip("symlinks unavailable")
				}
			}
			if err := s.Ensure(t.Context(), "stackctl-test.service", unit, owner); err == nil {
				t.Fatal("foreign service was adopted")
			}
			if len(*commands) != 0 {
				t.Fatal("conflicting service triggered a manager mutation")
			}
		})
	}
}

func TestServiceInstallDoesNotActivateModifiedUnitAfterReload(t *testing.T) {
	s, _, commands := serviceInstallFixture(t)
	original := s.run
	s.run = func(ctx context.Context, args ...string) error {
		err := original(ctx, args...)
		if args[0] == "daemon-reload" {
			os.WriteFile(filepath.Join(s.directory, "stackctl-test.service"), []byte("foreign-unit-content"), 0644)
		}
		return err
	}
	if err := s.Ensure(t.Context(), "stackctl-test.service", []byte("[Service]\nExecStart=/fixture/controller\n"), strings.Repeat("e", 32)); err == nil {
		t.Fatal("changed unit was activated")
	}
	if len(*commands) != 1 {
		t.Fatal("changed unit reached enable/start")
	}
}
