package host

import (
	"errors"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SupervisedCommand is a host-local helper with one writable directory. It does
// not install a unit, invoke a shell or communicate with a service manager.
type SupervisedCommand struct {
	Executable     string
	Arguments      []string
	WriteDirectory string
}

func linuxPath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value && value != "/" && unitLiteral(value)
}

func unitLiteral(value string) bool {
	return value != "" && len(value) <= 4096 && utf8.ValidString(value) &&
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) < 0
}

// RenderService emits a system unit for supported Debian/Ubuntu hosts. The ':'
// ExecStart prefix disables environment substitution; percent specifiers are
// escaped separately. Paths and arguments use systemd quoting, never shell
// quoting. The caller owns installation, verification and explicit activation.
func RenderService(command SupervisedCommand) ([]byte, error) {
	if !linuxPath(command.Executable) || !linuxPath(command.WriteDirectory) || len(command.Arguments) > 64 {
		return nil, errors.New("service requires canonical absolute Linux paths and bounded arguments")
	}
	if command.Executable == command.WriteDirectory || strings.HasPrefix(command.Executable, command.WriteDirectory+"/") {
		return nil, errors.New("service executable must be outside its writable directory")
	}
	quote := func(value string) string {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(value) + `"`
	}
	args := []string{quote(command.Executable)}
	for _, value := range command.Arguments {
		if !unitLiteral(value) {
			return nil, errors.New("service arguments must be bounded single-line literals")
		}
		args = append(args, quote(value))
	}
	unit := "[Unit]\nDescription=Stack controller helper\n\n[Service]\nType=exec\nExecStart=:" + strings.Join(args, " ") + "\n" +
		"Restart=on-failure\nRestartSec=5s\nTimeoutStopSec=15s\nUMask=0077\n" +
		"NoNewPrivileges=yes\nProtectSystem=strict\nPrivateDevices=yes\nCapabilityBoundingSet=\n" +
		"ReadWritePaths=" + quote(command.WriteDirectory) + "\nStandardOutput=journal\nStandardError=journal\n\n[Install]\nWantedBy=multi-user.target\n"
	return []byte(unit), nil
}
