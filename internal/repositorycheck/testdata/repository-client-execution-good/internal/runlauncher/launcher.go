package runlauncher

import (
	"os"
	"os/exec"
)

func start() error {
	_, _ = os.StartProcess("/bin/true", nil, &os.ProcAttr{})
	return exec.Command("claude").Run()
}
