package host

import (
	"bytes"
	"strings"
	"testing"
)

func TestServiceUsesLiteralArgumentsAndLimitsWritableScope(t *testing.T) {
	command := SupervisedCommand{
		Executable:     `/opt/releases/100% "quoted" $HOME/stackctl-go`,
		Arguments:      []string{"export-certificates", `literal\path "quote" $HOME %n`},
		WriteDirectory: "/opt/my stack/shared/certs",
	}
	data, err := RenderService(command)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(data)
	for _, required := range []string{
		`ExecStart=:"/opt/releases/100%% \"quoted\" $HOME/stackctl-go" "export-certificates" "literal\\path \"quote\" $HOME %%n"`,
		`ReadWritePaths="/opt/my stack/shared/certs"`,
		"ProtectSystem=strict\n", "NoNewPrivileges=yes\n", "CapabilityBoundingSet=\n", "Restart=on-failure\n", "UMask=0077\n", "Type=exec\n",
	} {
		if !strings.Contains(unit, required) {
			t.Fatalf("missing service behavior: %s", required)
		}
	}
	if strings.Contains(unit, "/bin/sh") || strings.Contains(unit, "EnvironmentFile=") || strings.Contains(unit, "ExecStartPre=") {
		t.Fatal("service added shell execution or implicit environment")
	}
	again, err := RenderService(command)
	if err != nil || !bytes.Equal(data, again) {
		t.Fatal("unit rendering is not deterministic")
	}
}

func TestServiceRejectsUnsafeOrAmbiguousPaths(t *testing.T) {
	for _, change := range []func(*SupervisedCommand){
		func(c *SupervisedCommand) { c.Executable = "relative" },
		func(c *SupervisedCommand) { c.WriteDirectory = "/" },
		func(c *SupervisedCommand) { c.WriteDirectory = "/opt/../tmp" },
		func(c *SupervisedCommand) { c.Executable = "/opt/certs/controller" },
		func(c *SupervisedCommand) { c.Arguments = []string{"private-marker\nExecStart=/bad"} },
		func(c *SupervisedCommand) { c.Executable = "/opt/private-marker\rcontroller" },
	} {
		command := SupervisedCommand{Executable: "/opt/bin/controller", Arguments: []string{"watch"}, WriteDirectory: "/opt/certs"}
		change(&command)
		if _, err := RenderService(command); err == nil || strings.Contains(err.Error(), "private-marker") {
			t.Fatal("invalid service accepted or echoed")
		}
	}
}
