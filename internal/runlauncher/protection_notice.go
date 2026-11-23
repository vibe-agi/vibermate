package runlauncher

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/locales"
)

// announceProtections states on stderr what this launch itself set for the
// client, and nothing else (ADR 0026). The launcher sets one protection, the
// instruction that leaves stream recovery to ViberMate. The Runtime applies
// the others, possibly at another version, and on Original Destination they
// have nothing to act on, so the command never names them. It writes at most
// two lines: an operator switch value it could not read, then the fallback
// instruction it set or the switch withheld.
func (launcher *Launcher) announceProtections(decision streamingFallbackDecision) {
	if launcher == nil || launcher.config.Stderr == nil {
		return
	}
	type line struct {
		key        string
		parameters map[string]string
	}
	switchParameters := map[string]string{
		"variable": clientadapter.RecognizedProtectionsVariable,
		"value":    displayedSwitchValue(decision.switchValue),
	}
	var lines []line
	if decision.switchUnrecognized {
		lines = append(lines, line{"cli.protections.switchUnrecognized", switchParameters})
	}
	switch {
	case decision.policy == clientadapter.StreamingFallbackCoreOwned:
		lines = append(lines, line{"cli.protections.streamingFallback", nil})
	case decision.switchedOff:
		lines = append(lines, line{"cli.protections.switchedOff", switchParameters})
	}
	if len(lines) == 0 {
		return
	}
	catalogs, err := locales.New()
	if err != nil {
		return
	}
	locale := locales.Detect(launcher.config.BaseEnvironment)
	for _, line := range lines {
		message, err := catalogs.Render(locale, line.key, line.parameters)
		if err == nil {
			_, _ = fmt.Fprintln(launcher.config.Stderr, message)
		}
	}
}

// displayedSwitchValue shows the operator's value as typed, quoted only when
// a space or an invisible character would otherwise hide what was typed or
// reach the terminal raw.
func displayedSwitchValue(value string) string {
	if value == "" || strings.IndexFunc(value, func(r rune) bool {
		return !unicode.IsGraphic(r) || unicode.IsSpace(r)
	}) >= 0 {
		return strconv.Quote(value)
	}
	return value
}
