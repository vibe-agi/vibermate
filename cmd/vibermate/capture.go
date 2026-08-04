package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/manualcaptureclient"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/locales"
)

const (
	keyCaptureUsage          = "cli.usage.captureCreate"
	keyCaptureContextFailed  = "cli.error.captureContextUnavailable"
	keyCaptureCreateFailed   = "cli.error.captureCreateFailed"
	keyCaptureOutcomeUnknown = "cli.error.captureOutcomeUnknown"
	keyCaptureDeliveryFailed = "cli.error.captureDeliveryFailed"
)

type captureCreateConfig struct {
	name         string
	clientClass  manualcapture.ClientClass
	lifetime     manualcapture.Lifetime
	expiresIn    time.Duration
	yes          bool
	outputFormat string
}

type durationFlag struct {
	value time.Duration
	set   bool
}

func (value *durationFlag) String() string {
	return value.value.String()
}

func (value *durationFlag) Set(raw string) error {
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return err
	}
	value.value = parsed
	value.set = true
	return nil
}

func executeCapture(
	arguments []string,
	environment []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) (int, string) {
	if len(arguments) == 0 || arguments[0] != "create" {
		return 2, keyCaptureUsage
	}
	config, err := parseCaptureCreate(arguments[1:])
	if err != nil {
		return 2, keyCaptureUsage
	}
	if !config.yes && !terminalInput(stdin) {
		return 2, keyCaptureUsage
	}
	layout, err := runtimepath.Default()
	if err != nil {
		return 1, keyRuntimePath
	}
	discovery, err := localdiscovery.NewFile(
		layout.CLIControlRecord,
		commandClock{},
	)
	if err != nil {
		return 1, keyRuntimePath
	}
	session, err := discovery.Load()
	if err != nil {
		return 1, keyRuntimeUnavailable
	}
	client, err := manualcaptureclient.New(session)
	if err != nil {
		return 1, keyCaptureContextFailed
	}
	defer client.Close()
	contextRequest, cancelContext := context.WithTimeout(
		context.Background(),
		15*time.Second,
	)
	captureContext, err := client.Context(contextRequest)
	cancelContext()
	if err != nil {
		return 1, keyCaptureContextFailed
	}
	catalogs, err := locales.New()
	if err != nil {
		return 1, reasonCatalogMissing
	}
	locale := locales.Detect(environment)
	if !config.yes {
		if err := renderCaptureReview(
			catalogs,
			locale,
			stderr,
			config,
			captureContext,
		); err != nil {
			return 1, reasonRenderFailed
		}
		confirmed, err := readConfirmation(stdin)
		if err != nil || !confirmed {
			return 0, ""
		}
	}
	expiresInSeconds := (*int64)(nil)
	if config.lifetime == manualcapture.LifetimeTemporary {
		seconds := int64(config.expiresIn / time.Second)
		expiresInSeconds = &seconds
	}
	createRequest, cancelCreate := context.WithTimeout(
		context.Background(),
		15*time.Second,
	)
	result, err := client.Create(createRequest, capturecontrol.ManualCaptureCreateRequest{
		DisplayName:       config.name,
		ClientClass:       config.clientClass,
		Lifetime:          config.lifetime,
		ExpiresInSeconds:  expiresInSeconds,
		ConfirmationToken: captureContext.ConfirmationToken,
	})
	cancelCreate()
	if err != nil {
		var failure *manualcaptureclient.Failure
		if errors.As(err, &failure) {
			return 1, keyCaptureCreateFailed
		}
		return 1, keyCaptureOutcomeUnknown
	}
	grant := result.Grant
	if grant.ProxyAddress != captureContext.ProxyAddress ||
		grant.Root != captureContext.Root {
		return 1, keyCaptureOutcomeUnknown
	}
	if err := renderCaptureGrant(
		catalogs,
		locale,
		stdout,
		config.outputFormat,
		grant,
	); err != nil {
		// Creation has already committed and the proxy password is intentionally
		// one-time. Do not report this as a generic rendering failure or invite a
		// blind retry that creates another credential.
		return 1, keyCaptureDeliveryFailed
	}
	return 0, ""
}

func parseCaptureCreate(arguments []string) (captureCreateConfig, error) {
	flags := flag.NewFlagSet("capture create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "")
	clientClass := flags.String("client", "cli", "")
	untilRevoked := flags.Bool("until-revoked", false, "")
	yes := flags.Bool("yes", false, "")
	outputFormat := flags.String("format", "human", "")
	expiresIn := durationFlag{value: manualcapture.DefaultTemporaryLifetime}
	flags.Var(&expiresIn, "expires-in", "")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 ||
		strings.TrimSpace(*name) != *name || *name == "" {
		return captureCreateConfig{}, errors.New("capture create arguments are invalid")
	}
	class := manualcapture.ClientClass(strings.ReplaceAll(*clientClass, "-", "_"))
	if !class.Valid() || (*outputFormat != "human" && *outputFormat != "shell") {
		return captureCreateConfig{}, errors.New("capture create option is invalid")
	}
	lifetime := manualcapture.LifetimeTemporary
	duration := expiresIn.value
	if *untilRevoked {
		if expiresIn.set {
			return captureCreateConfig{}, errors.New(
				"until-revoked and expires-in cannot be combined",
			)
		}
		lifetime = manualcapture.LifetimeUntilRevoked
		duration = 0
	}
	if lifetime == manualcapture.LifetimeTemporary &&
		(duration < manualcapture.MinimumTemporaryLifetime ||
			duration > manualcapture.MaximumTemporaryLifetime ||
			duration%time.Second != 0) {
		return captureCreateConfig{}, errors.New("capture lifetime is invalid")
	}
	return captureCreateConfig{
		name:         *name,
		clientClass:  class,
		lifetime:     lifetime,
		expiresIn:    duration,
		yes:          *yes,
		outputFormat: *outputFormat,
	}, nil
}

func renderCaptureReview(
	catalogs *locales.Catalogs,
	locale locales.Locale,
	output io.Writer,
	config captureCreateConfig,
	context capturecontrol.ManualCaptureContext,
) error {
	clientClass, err := catalogs.Render(
		locale,
		"cli.capture.client."+string(config.clientClass),
		nil,
	)
	if err != nil {
		return err
	}
	lifetime := config.expiresIn.String()
	if config.lifetime == manualcapture.LifetimeUntilRevoked {
		translated, err := catalogs.Render(locale, "cli.capture.untilRevoked", nil)
		if err != nil {
			return err
		}
		lifetime = translated
	}
	message, err := catalogs.Render(locale, "cli.capture.review", map[string]string{
		"name":            config.name,
		"clientClass":     clientClass,
		"lifetime":        lifetime,
		"proxyAddress":    context.ProxyAddress,
		"rootFingerprint": context.Root.Fingerprint,
		"rootPath":        context.Root.PEMPath,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, message)
	return err
}

func readConfirmation(input io.Reader) (bool, error) {
	line, err := bufio.NewReader(io.LimitReader(input, 64)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func terminalInput(input io.Reader) bool {
	file, ok := input.(*os.File)
	if !ok {
		// Injected readers are explicit test/application adapters. Production
		// stdin is always an *os.File and therefore follows the real mode check.
		return true
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func renderCaptureGrant(
	catalogs *locales.Catalogs,
	locale locales.Locale,
	output io.Writer,
	format string,
	grant capturecontrol.ManualCaptureGrant,
) error {
	proxyURL, err := proxyURLWithCredential(grant)
	if err != nil {
		return err
	}
	if format == "shell" {
		for _, entry := range [][2]string{
			{"HTTPS_PROXY", proxyURL},
			{"HTTP_PROXY", proxyURL},
			{"NODE_EXTRA_CA_CERTS", grant.Root.PEMPath},
			{"SSL_CERT_FILE", grant.Root.PEMPath},
		} {
			if _, err := fmt.Fprintf(
				output,
				"export %s=%s\n",
				entry[0],
				shellQuote(entry[1]),
			); err != nil {
				return err
			}
		}
		return nil
	}
	message, err := catalogs.Render(locale, "cli.capture.created", map[string]string{
		"captureId": grant.Capture.ID,
		"proxyUrl":  proxyURL,
		"rootPath":  grant.Root.PEMPath,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, message)
	return err
}

func proxyURLWithCredential(
	grant capturecontrol.ManualCaptureGrant,
) (string, error) {
	parsed, err := url.Parse(grant.ProxyAddress)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return "", errors.New("ManualCapture proxy address is invalid")
	}
	parsed.User = url.UserPassword(grant.ProxyUsername, grant.ProxyPassword)
	return parsed.String(), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
