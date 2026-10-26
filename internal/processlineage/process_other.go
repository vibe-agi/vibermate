//go:build !linux && !darwin

package processlineage

import "errors"

func (systemReader) Read(int) (Process, error) {
	return Process{}, errors.New("process lineage is unsupported on this platform")
}

func (systemReader) Environment(int) ([]string, error) {
	return nil, errors.New("process lineage is unsupported on this platform")
}

func (systemReader) Processes() ([]int, error) {
	return nil, errors.New("process lineage is unsupported on this platform")
}
