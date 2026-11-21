package clientadapter

import process "os"

func help(path string) error {
	_, err := process.StartProcess(path, []string{path, "--help"}, &process.ProcAttr{})
	return err
}
