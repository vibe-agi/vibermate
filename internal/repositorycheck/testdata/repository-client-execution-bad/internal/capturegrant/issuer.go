package capturegrant

import "os/exec"

func clientVersion(path string) ([]byte, error) {
	return exec.Command(path, "--version").Output()
}
