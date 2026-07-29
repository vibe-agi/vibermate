// Command repositorycheck runs deterministic implementation repository gates.
package main

import (
	"fmt"
	"os"

	"github.com/vibe-agi/vibermate/internal/repositorycheck"
)

func main() {
	if err := repositorycheck.Check("."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
