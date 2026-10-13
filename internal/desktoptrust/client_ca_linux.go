//go:build linux

package desktoptrust

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func installClientCertificate(ctx context.Context, path, fingerprint string) error {
	var destination, update string
	for _, choice := range []struct{ directory, tool string }{
		{"/usr/local/share/ca-certificates", "/usr/sbin/update-ca-certificates"},
		{"/etc/pki/ca-trust/source/anchors", "/usr/bin/update-ca-trust"},
	} {
		if info, err := os.Stat(choice.directory); err == nil && info.IsDir() {
			if info, err := os.Stat(choice.tool); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
				destination, update = choice.directory, choice.tool
				break
			}
		}
	}
	if destination == "" {
		return errors.New("this Linux distribution has no supported CA update tool")
	}
	install := "/usr/bin/install"
	if _, err := os.Stat(install); err != nil {
		install = "/bin/install"
	}
	destination = filepath.Join(destination, "vibermate-"+fingerprint+".crt")
	run := func(executable string, args ...string) error {
		if os.Geteuid() != 0 {
			args = append([]string{executable}, args...)
			executable = "/usr/bin/sudo"
		}
		command := exec.CommandContext(ctx, executable, args...)
		command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, io.Discard, os.Stderr
		if err := command.Run(); err != nil {
			return errors.New("CA installation requires administrator permission and the native CA update tool")
		}
		return nil
	}
	if err := run(install, "-m", "0644", path, destination); err != nil {
		return err
	}
	return run(update)
}
