// Command vibermate-wirecapture runs a loopback-only development endpoint for
// recording value-only TLS and HTTP presentation evidence from fixed clients.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vibe-agi/vibermate/internal/wirecapture"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	flags := flag.NewFlagSet("vibermate-wirecapture", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listenAddress := flags.String(
		"listen",
		"127.0.0.1:0",
		"loopback TCP address",
	)
	certificatePath := flags.String("cert", "", "TLS certificate PEM path")
	privateKeyPath := flags.String("key", "", "TLS private key PEM path")
	outputPath := flags.String("output", "", "owner-private JSON report path")
	sampleLimit := flags.Int("samples", 1, "number of completed requests to capture")
	timeout := flags.Duration("timeout", 30*time.Second, "per-connection timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("wire capture does not accept positional arguments")
	}
	if *certificatePath == "" || *privateKeyPath == "" || *outputPath == "" {
		return errors.New("wire capture requires --cert, --key, and --output")
	}
	absoluteOutput, err := filepath.Abs(*outputPath)
	if err != nil {
		return fmt.Errorf("resolve wire capture output path: %w", err)
	}
	identity, err := tls.LoadX509KeyPair(*certificatePath, *privateKeyPath)
	if err != nil {
		return fmt.Errorf("load wire capture TLS identity: %w", err)
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return fmt.Errorf("listen for wire capture: %w", err)
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.IP == nil || !address.IP.IsLoopback() {
		return errors.New("wire capture --listen must resolve to loopback")
	}
	if _, err := fmt.Fprintf(stdout, "listen=%s\n", listener.Addr().String()); err != nil {
		return err
	}
	report, err := wirecapture.Capture(ctx, listener, wirecapture.Options{
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{identity},
			MinVersion:   tls.VersionTLS12,
		},
		SampleLimit: *sampleLimit,
		Timeout:     *timeout,
	})
	if err != nil {
		return err
	}
	if err := wirecapture.WriteReport(absoluteOutput, report); err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		stdout,
		"report=%s samples=%d\n",
		absoluteOutput,
		len(report.Samples),
	)
	return err
}
