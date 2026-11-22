package capturegrant

import (
	"os/exec"
	"testing"
)

func TestInstalledClient(t *testing.T) {
	_ = exec.Command("claude", "--version")
}
