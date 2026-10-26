package processlineage

import (
	"encoding/binary"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcargsEnvironmentSkipsPathPaddingAndArguments(t *testing.T) {
	data := binary.LittleEndian.AppendUint32(nil, 2)
	data = append(data, "/usr/bin/codex\x00\x00\x00\x00"...)
	data = append(data, "codex\x00--yolo\x00"...)
	data = append(data, "HOME=/Users/a\x00VIBERMATE_TRANSPARENT_CAPTURE=t1\x00\x00"...)
	data = append(data, "executable_path=/usr/bin/codex\x00"...) // Apple strings follow.
	got, err := procargsEnvironment(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"HOME=/Users/a", "VIBERMATE_TRANSPARENT_CAPTURE=t1"}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %q, want %q", got, want)
	}
	for _, malformed := range [][]byte{nil, {1, 0, 0}, binary.LittleEndian.AppendUint32(nil, 3)} {
		if _, err := procargsEnvironment(malformed); err == nil {
			t.Errorf("procargsEnvironment(%q) succeeded", malformed)
		}
	}
}

type markedTable struct {
	*fakeTable
	environments map[int][]string
}

func (table markedTable) Environment(pid int) ([]string, error) {
	table.mu.Lock()
	defer table.mu.Unlock()
	if _, live := table.processes[pid]; !live {
		return nil, ErrNotFound
	}
	return table.environments[pid], nil
}

func TestMarkerAttributesAProcessOrphanedBeforeItsFirstConnection(t *testing.T) {
	base, _ := newFakeTree()
	table := markedTable{fakeTable: base, environments: map[int][]string{}}
	registry := NewRegistry(table)
	if err := registry.RegisterWithMarker(Identity{PID: 100, Start: 100}, "run-a", "VMT=a"); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterWithMarker(Identity{PID: 200, Start: 200}, "run-b", "VMT=b"); err != nil {
		t.Fatal(err)
	}
	// A daemon double-forked from claude: its parent exited before it ever
	// connected, so launchd is its only ancestor.
	table.set(400, 1, 400)
	table.environments[400] = []string{"HOME=/h", "VMT=a"}
	wantOwner(t, registry, 400, "run-a")
	// Once attributed it stays attributed, as a walked descendant does.
	table.environments[400] = nil
	wantOwner(t, registry, 400, "run-a")

	// Lineage wins over an inherited marker: a nested run's descendants
	// belong to the nested run.
	table.environments[201] = []string{"VMT=a"}
	wantOwner(t, registry, 201, "run-b")

	// Unrelated, unmarked and foreign-marked processes stay unowned.
	table.set(401, 1, 401)
	wantOwner(t, registry, 401, "")
	table.set(402, 1, 402)
	table.environments[402] = []string{"VMT=unregistered"}
	wantOwner(t, registry, 402, "")

	// A launcher carrying an outer run's marker is still not owned itself.
	table.environments[200] = []string{"VMT=a"}
	wantOwner(t, registry, 200, "")

	// Deregistering forgets the marker.
	registry.Deregister("run-a")
	table.set(403, 1, 403)
	table.environments[403] = []string{"VMT=a"}
	wantOwner(t, registry, 403, "")
}

func TestMarkerBelongsToOneRun(t *testing.T) {
	base, _ := newFakeTree()
	registry := NewRegistry(markedTable{fakeTable: base, environments: map[int][]string{}})
	if err := registry.RegisterWithMarker(Identity{PID: 100, Start: 100}, "run-a", "VMT=a"); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterWithMarker(Identity{PID: 200, Start: 200}, "run-b", "VMT=a"); err == nil {
		t.Fatal("a second run claimed another run's marker")
	}
	if err := registry.RegisterWithMarker(Identity{PID: 200, Start: 200}, "run-b", "no-equals"); err == nil {
		t.Fatal("a marker that is not KEY=value was accepted")
	}
}

// A real background child whose parent shell exits before it is looked up is
// found by the marker its environment inherited.
func TestSystemMarkerFindsARealOrphan(t *testing.T) {
	self, err := System().Read(os.Getpid())
	if err != nil {
		t.Skipf("process table unavailable: %v", err)
	}
	if _, err := System().(EnvironmentReader).Environment(os.Getpid()); err != nil {
		t.Skipf("environment unavailable: %v", err)
	}
	registry := NewRegistry(System())
	marker := "VIBERMATE_TEST_MARKER=" + strconv.Itoa(os.Getpid())
	if err := registry.RegisterWithMarker(self.Identity, "run-test", marker); err != nil {
		t.Fatal(err)
	}
	orphan := func(environment []string) int {
		t.Helper()
		shell := exec.Command("sh", "-c", "sleep 30 </dev/null >/dev/null 2>&1 & echo $!")
		shell.Env = environment
		output, err := shell.Output()
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
			}
		})
		// The shell has exited: the sleeper's parent is no longer this test.
		deadline := time.Now().Add(5 * time.Second)
		for {
			process, err := System().Read(pid)
			if err == nil && process.Parent != shell.Process.Pid && process.Parent != os.Getpid() {
				return pid
			}
			if time.Now().After(deadline) {
				t.Skipf("orphan %d was not reparented (parent %d)", pid, process.Parent)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	marked := orphan(append(os.Environ(), marker))
	wantOwner(t, registry, marked, "run-test")
	unmarked := orphan(os.Environ())
	wantOwner(t, registry, unmarked, "")
}
