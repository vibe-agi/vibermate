package capturerun

import . "syscall"

func replaceSelf(path string) error {
	return Exec(path, nil, nil)
}
