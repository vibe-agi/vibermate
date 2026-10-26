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
	// After ")": state(3) ppid(4) pgrp(5) ... starttime(22); index 0 here is
	// field 3.
	fields := bytes.Fields(data[end+1:])
	if len(fields) < 20 {
		return Process{}, fmt.Errorf("process %d stat is malformed", pid)
	}
	parent, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return Process{}, fmt.Errorf("process %d parent is malformed", pid)
	}
	group, err := strconv.Atoi(string(fields[2]))
	if err != nil {
		return Process{}, fmt.Errorf("process %d group is malformed", pid)
	}
	start, err := strconv.ParseInt(string(fields[19]), 10, 64)
	if err != nil {
		return Process{}, fmt.Errorf("process %d start time is malformed", pid)
	}
	stopped := len(fields[0]) == 1 && (fields[0][0] == 'T' || fields[0][0] == 't')
	return Process{Identity: Identity{PID: pid, Start: start}, Parent: parent, Group: group, Stopped: stopped}, nil
}

// Environment reads /proc/<pid>/environ, which only the process's own user
// may read: the environment it was started with.
func (systemReader) Environment(pid int) ([]string, error) {
	if pid <= 0 {
		return nil, ErrNotFound
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return splitNUL(data), nil
}

// Processes lists /proc. Other users' processes are listed too; reading
// their environment fails, so Marked skips them.
func (systemReader) Processes() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, entry := range entries {
		if pid, err := strconv.Atoi(entry.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
