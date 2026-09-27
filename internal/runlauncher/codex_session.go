package runlauncher

import (
	"fmt"
	"os"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/locales"
	"golang.org/x/term"
)

// Keep session selection and ownership in Codex. Parsing `resume UUID` here
// misses --last, names, pickers, and /resume after launch. Native R/F/exit work
// at the shared resume boundary, without stealing a writer or hiding a fork.
func (launcher *Launcher) announceCodexSessions(recipe clientadapter.LaunchRecipe) {
	if recipe != clientadapter.LaunchCodexResponsesHTTP {
		return
	}
	input, inOK := launcher.config.Stdin.(*os.File)
	output, outOK := launcher.config.Stderr.(*os.File)
	if !inOK || !outOK || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(output.Fd())) {
		return // Do not add interaction or diagnostics to pipe-based clients.
	}
	catalogs, err := locales.New()
	if err != nil {
		return
	}
	message, err := catalogs.Render(locales.Detect(launcher.config.BaseEnvironment), "cli.codexSession.nativeChoices", nil)
	if err == nil {
		_, _ = fmt.Fprintln(output, message)
	}
}
