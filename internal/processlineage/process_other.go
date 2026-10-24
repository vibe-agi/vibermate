//go:build !linux && !darwin

package processlineage

import "errors"

func (systemReader) Read(int) (Process, error) {
	return Process{}, errors.New("process lineage is unsupported on this platform")
}
