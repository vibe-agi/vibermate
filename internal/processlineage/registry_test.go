package processlineage

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTable struct {
	mu        sync.Mutex
	processes map[int]Process
}

func (table *fakeTable) Read(pid int) (Process, error) {
	table.mu.Lock()
	defer table.mu.Unlock()
	process, ok := table.processes[pid]
	if !ok {
		return Process{}, ErrNotFound
	}
	return process, nil
}

func (table *fakeTable) set(pid, parent int, start int64) Identity {
	table.mu.Lock()
	defer table.mu.Unlock()
	identity := Identity{PID: pid, Start: start}
	table.processes[pid] = Process{Identity: identity, Parent: parent}
	return identity
}

func (table *fakeTable) setGroup(pid, group int) {
	table.mu.Lock()
	defer table.mu.Unlock()
	process := table.processes[pid]
	process.Group = group
	table.processes[pid] = process
}

func (table *fakeTable) remove(pid int) {
	table.mu.Lock()
	defer table.mu.Unlock()
	delete(table.processes, pid)
}

// The table: launchd(1) → terminal(10) → run A root(100) → claude(101) → bash(102)
//
//	                           → run B root(200) → codex(201)
//	terminal(10) → unrelated curl(300)
func newFakeTree() (*fakeTable, *Registry) {
	table := &fakeTable{processes: map[int]Process{}}
	table.set(1, 0, 1)
	table.set(10, 1, 10)
	table.set(100, 10, 100)
	table.set(101, 100, 101)
	table.set(102, 101, 102)
	table.set(200, 10, 200)
	table.set(201, 200, 201)
	table.set(300, 10, 300)
	return table, NewRegistry(table)
}

func wantOwner(t *testing.T, registry *Registry, pid int, run string) {
	t.Helper()
	got, owned := registry.Owner(pid)
	if run == "" && owned {
		t.Fatalf("Owner(%d) = %q, want unowned", pid, got)
	}
	if run != "" && got != run {
		t.Fatalf("Owner(%d) = %q (owned %t), want %q", pid, got, owned, run)
	}
}

func TestConcurrentRunsOwnOnlyTheirDescendants(t *testing.T) {
	_, registry := newFakeTree()
	if err := registry.Register(Identity{PID: 100, Start: 100}, "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Identity{PID: 200, Start: 200}, "run-b"); err != nil {
		t.Fatal(err)
	}
	wantOwner(t, registry, 101, "run-a")
	wantOwner(t, registry, 102, "run-a")
	wantOwner(t, registry, 201, "run-b")
	wantOwner(t, registry, 300, "")
	wantOwner(t, registry, 10, "")
	// A launcher's own connections are not captured.
	wantOwner(t, registry, 100, "")
	wantOwner(t, registry, 200, "")
}

func TestRememberedDescendantSurvivesReparenting(t *testing.T) {
	table, registry := newFakeTree()
	if err := registry.Register(Identity{PID: 100, Start: 100}, "run-a"); err != nil {
		t.Fatal(err)
	}
	wantOwner(t, registry, 102, "run-a")
	// bash daemonizes: its parent exits and launchd adopts it.
	table.remove(101)
	table.set(102, 1, 102)
	wantOwner(t, registry, 102, "run-a")
	// A process never seen before reparenting cannot be attributed.
	table.set(103, 1, 103)
	wantOwner(t, registry, 103, "")
}

func TestReusedPIDIsADifferentProcess(t *testing.T) {
	table, registry := newFakeTree()
	if err := registry.Register(Identity{PID: 100, Start: 100}, "run-a"); err != nil {
		t.Fatal(err)
	}
	wantOwner(t, registry, 102, "run-a")
	table.remove(102)
	table.set(102, 10, 999)
	wantOwner(t, registry, 102, "")
}

func TestDeregisterForgetsRunAndLearnedDescendants(t *testing.T) {
	table, registry := newFakeTree()
	if err := registry.Register(Identity{PID: 100, Start: 100}, "run-a"); err != nil {
		t.Fatal(err)
	}
	wantOwner(t, registry, 102, "run-a")
	table.set(102, 1, 102)
	registry.Deregister("run-a")
	wantOwner(t, registry, 102, "")
	wantOwner(t, registry, 101, "")
}

func TestRegisterRequiresTheLiveRoot(t *testing.T) {
	_, registry := newFakeTree()
	for _, root := range []Identity{{PID: 100, Start: 99}, {PID: 999, Start: 1}, {PID: 1, Start: 1}} {
		if err := registry.Register(root, "run"); err == nil {
			t.Fatalf("Register(%+v) succeeded", root)
		}
	}
	if err := registry.Register(Identity{PID: 100, Start: 100}, "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Identity{PID: 100, Start: 100}, "run-b"); err == nil {
		t.Fatal("a second run took an existing root")
	}
}

func TestParentWalkIsBounded(t *testing.T) {
	table := &fakeTable{processes: map[int]Process{}}
	table.set(2, 3, 2)
	table.set(3, 2, 3)
	registry := NewRegistry(table)
	wantOwner(t, registry, 2, "")
}

func TestSystemReaderFollowsRealChildren(t *testing.T) {
	self, err := System().Read(os.Getpid())
	if err != nil {
		t.Skipf("process table unavailable: %v", err)
	}
	if self.PID != os.Getpid() || self.Parent != os.Getppid() {
		t.Fatalf("Read(self) = %+v", self)
	}
	registry := NewRegistry(System())
	if err := registry.Register(self.Identity, "run-test"); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-c", "sleep 30 & echo $!; wait")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	buffer := make([]byte, 32)
	n, err := stdout.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(buffer[:n])))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if run, owned := registry.Owner(grandchild); owned && run == "run-test" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d was not attributed", grandchild)
		}
		time.Sleep(10 * time.Millisecond)
	}
	wantOwner(t, registry, command.Process.Pid, "run-test")
	wantOwner(t, registry, os.Getpid(), "")
	wantOwner(t, registry, os.Getppid(), "")
}

// An orphan whose environment is unreadable (a macOS platform binary) is
// still owned through the process group its Agent leads, and only while that
// Agent lives. Groups led by a root or by an unrelated process own nothing.
func TestOwnerAttributesAnOrphanByItsAgentsGroup(t *testing.T) {
	table, registry := newFakeTree()
	if err := registry.Register(Identity{PID: 100, Start: 100}, "run-a"); err != nil {
		t.Fatal(err)
	}
	table.setGroup(101, 101) // claude leads its own group
	table.set(400, 1, 400)   // an orphaned curl, reparented to launchd
	table.setGroup(400, 101)
	wantOwner(t, registry, 400, "run-a")

	table.set(401, 1, 401) // an orphan in the root's own group
	table.setGroup(100, 100)
	table.setGroup(401, 100)
	wantOwner(t, registry, 401, "")

	table.set(402, 1, 402) // an orphan in an unrelated terminal job
	table.setGroup(300, 300)
	table.setGroup(402, 300)
	wantOwner(t, registry, 402, "")

	table.set(403, 1, 403) // the Agent has exited: its group names nothing
	table.setGroup(403, 101)
	table.remove(101)
	wantOwner(t, registry, 403, "")
}
