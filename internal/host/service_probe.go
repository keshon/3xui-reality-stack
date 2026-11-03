package host

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type ServiceObservation struct {
	LoadState   string `json:"load_state"`
	ActiveState string `json:"active_state"`
	Fragment    string `json:"fragment"`
	DropIns     string `json:"drop_ins"`
	FileState   string `json:"file_state"`
}

func (s ServiceObservation) Absent() bool {
	return s.LoadState == "not-found" && s.ActiveState == "inactive" && s.Fragment == "" && s.DropIns == "" && (s.FileState == "" || s.FileState == "not-found")
}

var serviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,100}\.service$`)

// ObserveService reads the system manager's loaded unit state. An absent result
// is a snapshot, not a reservation; installation must recheck it before writing.
func ObserveService(ctx context.Context, name string) (ServiceObservation, error) {
	if runtime.GOOS != "linux" {
		return ServiceObservation{}, errors.New("service observation requires Linux")
	}
	return observeService(ctx, name, systemctlRead)
}

func observeService(ctx context.Context, name string, run func(context.Context, ...string) ([]byte, error)) (ServiceObservation, error) {
	if err := ctx.Err(); err != nil {
		return ServiceObservation{}, err
	}
	if !serviceName.MatchString(name) {
		return ServiceObservation{}, errors.New("invalid service name")
	}
	data, err := run(ctx, "--system", "--no-pager", "show", "--property=LoadState,ActiveState,FragmentPath,DropInPaths,UnitFileState", "--", name)
	if err != nil || len(data) > 1<<20 {
		return ServiceObservation{}, errors.New("system service ownership could not be established")
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return ServiceObservation{}, errors.New("invalid system service observation")
		}
		if _, exists := values[key]; exists {
			return ServiceObservation{}, errors.New("duplicate system service observation")
		}
		values[key] = value
	}
	for _, key := range []string{"LoadState", "ActiveState", "FragmentPath", "DropInPaths", "UnitFileState"} {
		if _, ok := values[key]; !ok {
			return ServiceObservation{}, errors.New("incomplete system service observation")
		}
	}
	if len(values) != 5 {
		return ServiceObservation{}, errors.New("unexpected system service observation")
	}
	return ServiceObservation{values["LoadState"], values["ActiveState"], values["FragmentPath"], values["DropInPaths"], values["UnitFileState"]}, nil
}

func systemctlRead(ctx context.Context, args ...string) ([]byte, error) {
	return systemctlLocal(ctx, true, args...)
}

func systemctlLocal(ctx context.Context, allowMissing bool, args ...string) ([]byte, error) {
	timeout := 30 * time.Second
	if allowMissing {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	cmd.Env = []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "DBUS_") && !strings.HasPrefix(key, "SYSTEMD_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	var out limitedOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if !allowMissing || !errors.As(err, &exit) || exit.ExitCode() != 4 {
			return nil, errors.New("system service manager unavailable")
		}
	}
	return out.Bytes(), nil
}
