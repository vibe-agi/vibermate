package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientpath"
	"github.com/vibe-agi/vibermate/internal/desktoptrust"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
)

type caConfig struct {
	action, server, out, fingerprint string
	yes                              bool
}

func parseCA(arguments []string) (caConfig, error) {
	var config caConfig
	if len(arguments) < 1 {
		return config, fmt.Errorf("expected ca install, ca status or ca download")
	}
	config.action = arguments[0]
	flags := flag.NewFlagSet("ca", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&config.server, "server", "", "")
	flags.StringVar(&config.out, "out", "", "")
	flags.StringVar(&config.fingerprint, "fingerprint", "", "")
	flags.BoolVar(&config.yes, "yes", false, "")
	if flags.Parse(arguments[1:]) != nil || flags.NArg() != 0 || config.server == "" ||
		(config.action != "install" && config.action != "status" && config.action != "download") ||
		((config.action == "download") != (config.out != "")) || (config.action != "install" && config.yes) {
		return config, fmt.Errorf("invalid CA command")
	}
	target, err := serverconnection.ParseTarget(config.server)
	if err != nil {
		return config, err
	}
	if config.fingerprint != "" {
		decoded, e := hex.DecodeString(config.fingerprint)
		if e != nil || len(decoded) != 32 || strings.ToLower(config.fingerprint) != config.fingerprint {
			return config, fmt.Errorf("invalid CA fingerprint")
		}
	}
	if config.yes && target.Transport() == serverconnection.TransportHTTP && config.fingerprint == "" {
		return config, fmt.Errorf("HTTP CA installation with --yes requires --fingerprint")
	}
	return config, nil
}

func executeCA(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer) (int, string) {
	config, err := parseCA(arguments)
	if err != nil {
		return 2, "cli.usage.ca"
	}
	target, _ := serverconnection.ParseTarget(config.server)
	directory, err := clientpath.DefaultRemoteStateDirectory()
	if err != nil {
		return 1, keyRuntimePath
	}
	material, err := runlauncher.RemoteRootCertificate(ctx, runlauncher.RemoteConfig{Target: target, StateDirectory: directory, Clock: commandClock{}, Random: rand.Reader, DisplayName: "vibermate-client"}, 15*time.Second)
	if err != nil {
		var failure *runlauncher.ControlFailure
		if errors.As(err, &failure) {
			fmt.Fprintln(stderr, failure.SafeSummary())
			if failure.Status == 401 {
				return 1, "cli.error.caDownloadDenied"
			}
		}
		key := launchFailureKey(err)
		if key == keyLaunchFailed {
			key = "cli.error.caFailed"
		}
		return 1, key
	}
	certificate, err := runlauncher.ValidatePublicCA([]byte(material.CertificatePEM), material.Fingerprint, time.Now().UTC())
	if err != nil {
		return 1, "cli.error.caFailed"
	}
	if config.fingerprint != "" && config.fingerprint != material.Fingerprint {
		fmt.Fprintln(stderr, "CA fingerprint does not match. No trust settings changed.")
		return 1, "cli.error.caFailed"
	}
	fmt.Fprintf(stdout, "Server: %s\nPlatform: %s\nCA SHA-256: %s\n", target.Origin(), desktoptrust.ClientCAPlatform(), material.Fingerprint)
	if config.action == "status" {
		if desktoptrust.ClientCAStatus(certificate) != nil {
			fmt.Fprintln(stdout, "This CA is not trusted on this device.")
			return 1, "cli.error.caUntrusted"
		}
		fmt.Fprintln(stdout, "This CA is trusted on this device.")
		return 0, ""
	}
	if config.action == "download" {
		file, e := os.OpenFile(config.out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return 1, "cli.error.caFailed"
		}
		_, e = file.WriteString(material.CertificatePEM)
		closeErr := file.Close()
		if e != nil || closeErr != nil {
			return 1, "cli.error.caFailed"
		}
		fmt.Fprintf(stdout, "Saved the public CA certificate to %s. No trust settings changed.\n", config.out)
		return 0, ""
	}
	if !config.yes {
		if target.Transport() == serverconnection.TransportHTTP {
			fmt.Fprintln(stderr, "HTTP does not authenticate this CA. Check the fingerprint with the server owner first.")
			fmt.Fprint(stdout, "To install, enter the exact CA SHA-256 shown by the trusted server owner: ")
			line, e := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
			if e != nil && e != io.EOF {
				return 1, "cli.error.caFailed"
			}
			if strings.TrimSpace(line) != material.Fingerprint {
				fmt.Fprintln(stdout, "Cancelled. No trust settings changed.")
				return 1, "cli.error.caFailed"
			}
		} else {
			fmt.Fprintln(stdout, "Installing this CA allows HTTPS traffic inspection on this device. Type yes to continue:")
			line, _ := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
			if strings.TrimSpace(line) != "yes" {
				fmt.Fprintln(stdout, "Cancelled. No trust settings changed.")
				return 1, "cli.error.caFailed"
			}
		}
	}
	if err := desktoptrust.InstallClientCA(ctx, certificate); err != nil {
		fmt.Fprintln(stderr, err)
		return 1, "cli.error.caFailed"
	}
	fmt.Fprintln(stdout, "CA installed and verified. Restart clients that were already running.")
	return 0, ""
}
