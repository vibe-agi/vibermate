//go:build linux

package processlineage

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
)

// Read parses /proc/<pid>/stat. The command name is parenthesized and may
// contain spaces or parentheses, so fields are counted after the last ')'.
func (systemReader) Read(pid int) (Process, error) {
	if pid <= 0 {
		return Process{}, ErrNotFound
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) {
		return Process{}, ErrNotFound
	}
	if err != nil {
		return Process{}, err
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return Process{}, fmt.Errorf("process %d stat is malformed", pid)
	}
	// After ")": state(3) ppid(4) ... starttime(22); index 0 here is field 3.
	fields := bytes.Fields(data[end+1:])
	if len(fields) < 20 {
		return Process{}, fmt.Errorf("process %d stat is malformed", pid)
	}
	parent, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return Process{}, fmt.Errorf("process %d parent is malformed", pid)
	}
	start, err := strconv.ParseInt(string(fields[19]), 10, 64)
	if err != nil {
		return Process{}, fmt.Errorf("process %d start time is malformed", pid)
	}
	return Process{Identity: Identity{PID: pid, Start: start}, Parent: parent}, nil
}
