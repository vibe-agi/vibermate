package capturegrant

import "os"

// A process package is fine here for anything that does not start a process.
func dataDirectory() string {
	return os.Getenv("HOME")
}
