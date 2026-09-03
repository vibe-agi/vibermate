package filetransaction

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

const (
	transactionHelperPath  = "VIBERMATE_FILE_TRANSACTION_HELPER_PATH"
	transactionHelperGate  = "VIBERMATE_FILE_TRANSACTION_HELPER_GATE"
	transactionHelperValue = "VIBERMATE_FILE_TRANSACTION_HELPER_VALUE"
)

func TestFileTransactionSerializesIndependentProcesses(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	gate := filepath.Join(directory, "start")
	if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}

	type runningHelper struct {
		command *exec.Cmd
		output  *bytes.Buffer
	}
	commands := make([]runningHelper, 0, 2)
	for _, value := range []string{"left", "right"} {
		command := exec.Command(
			os.Args[0],
			"-test.run=^TestFileTransactionProcessHelper$",
		)
		command.Env = append(
			os.Environ(),
			transactionHelperPath+"="+path,
			transactionHelperGate+"="+gate,
			transactionHelperValue+"="+value,
		)
		output := &bytes.Buffer{}
		command.Stdout = output
		command.Stderr = output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, runningHelper{command: command, output: output})
	}
	if err := os.WriteFile(gate, []byte("start"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := range commands {
		if err := commands[index].command.Wait(); err != nil {
			t.Fatalf("helper %d: %v\n%s", index, err, commands[index].output.Bytes())
		}
	}

	snapshot, err := Read(transactionTestOptions(path))
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	if err := json.Unmarshal(snapshot.Payload, &values); err != nil {
		t.Fatal(err)
	}
	sort.Strings(values)
	if len(values) != 2 || values[0] != "left" || values[1] != "right" {
		t.Fatalf("cross-process values = %v", values)
	}
}

func TestFileTransactionProcessHelper(t *testing.T) {
	path := os.Getenv(transactionHelperPath)
	if path == "" {
		t.Skip("helper process only")
	}
	gate := os.Getenv(transactionHelperGate)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("start gate did not open")
		}
		time.Sleep(5 * time.Millisecond)
	}
	value := os.Getenv(transactionHelperValue)
	if err := Update(
		transactionTestOptions(path),
		func(snapshot Snapshot) (Mutation, error) {
			var values []string
			if err := json.Unmarshal(snapshot.Payload, &values); err != nil {
				return Mutation{}, err
			}
			values = append(values, value)
			// Enlarge the stale-read window: without the kernel lock both helper
			// processes commit a one-entry document.
			time.Sleep(150 * time.Millisecond)
			payload, err := json.Marshal(values)
			return Mutation{Payload: payload, Write: true}, err
		},
	); err != nil {
		t.Fatal(err)
	}
}

func transactionTestOptions(path string) Options {
	return Options{
		Path: path, MaximumBytes: 4096, Mode: 0o600,
		TemporaryPrefix: ".state-*",
	}
}
