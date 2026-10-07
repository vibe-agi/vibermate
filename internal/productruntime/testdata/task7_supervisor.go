//go:build ignore

// Task7's finite, directly owned command driver. Every invocation writes an
// exit receipt for its original exec.Cmd and retains combined child output.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 5 {
		panic("usage: supervisor ROOT LABEL WORKDIR COMMAND [ARGS...]")
	}
	root, label, dir := os.Args[1], os.Args[2], os.Args[3]
	if !filepath.IsAbs(root) || !strings.HasPrefix(filepath.Base(root), "vibermate-task7.") {
		panic("invalid evidence root")
	}
	hashes := map[string]string{}
	files := []string{os.Args[4], filepath.Join(dir, "go.mod"), filepath.Join(dir, "go.sum")}
	adapters, _ := filepath.Glob(filepath.Join(dir, "internal/productruntime/*acceptance_test.go"))
	files = append(files, adapters...)
	for _, name := range files {
		if b, e := os.ReadFile(name); e == nil {
			hashes[name] = fmt.Sprintf("%x", sha256.Sum256(b))
			if strings.HasSuffix(name, ".go") {
				if e := os.WriteFile(filepath.Join(root, label+"-"+filepath.Base(name)), b, 0600); e != nil {
					panic(e)
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	log, e := os.OpenFile(filepath.Join(root, label+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		panic(e)
	}
	cmd := exec.CommandContext(ctx, os.Args[4], os.Args[5:]...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	cmd.Stdout = log
	cmd.Stderr = log
	start := time.Now().UTC()
	e = cmd.Start()
	pid := 0
	reaped := false
	if e == nil {
		pid = cmd.Process.Pid
		e = cmd.Wait()
		reaped = true
	}
	log.Close()
	data, _ := os.ReadFile(filepath.Join(root, label+".log"))
	exit := -1
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	for i, a := range cmd.Args {
		if a == "-o" && i+1 < len(cmd.Args) {
			if b, e := os.ReadFile(cmd.Args[i+1]); e == nil {
				hashes[cmd.Args[i+1]] = fmt.Sprintf("%x", sha256.Sum256(b))
			}
		}
	}
	receipt := map[string]any{"argv": cmd.Args, "directory": dir, "started": start, "finished": time.Now().UTC(), "pid": pid, "reaped": reaped, "exit": exit, "context_error": fmt.Sprint(ctx.Err()), "error": fmt.Sprint(e), "log_sha256": fmt.Sprintf("%x", sha256.Sum256(data)), "file_sha256": hashes}
	b, _ := json.MarshalIndent(receipt, "", "  ")
	if writeErr := os.WriteFile(filepath.Join(root, label+".json"), b, 0600); writeErr != nil {
		panic(writeErr)
	}
	fmt.Println(string(b))
	if e != nil {
		os.Exit(1)
	}
}
