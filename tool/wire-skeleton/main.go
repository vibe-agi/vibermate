// Command wire-skeleton turns a real client request dump into a fixture that
// can be committed: a Go port of the analysis script skeleton.py, extended so
// the result still decodes the way the original did.
//
// Every value ViberMate interprets (types, roles, models, ids, tool names,
// schemas, signatures, numbers, member order) is kept byte for byte. Prose
// longer than the clip length (prompts, tool descriptions, long tool output)
// becomes a deterministic placeholder of exactly the same byte length; UUIDs,
// long hex identifiers, the attribution hash and temporary or home paths are
// replaced by deterministic look-alikes; base64 media become tiny valid files
// of the same media type. Request heads are never read.
//
// Usage:
//
//	wire-skeleton [-clip N] [-redact VALUE]... INPUT OUTPUT
//
// INPUT is a JSON body, or a server-sent event stream when it ends in .sse.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

type redactions []string

func (values *redactions) String() string { return strings.Join(*values, ",") }

func (values *redactions) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	options := DefaultOptions()
	var redact redactions
	flags := flag.NewFlagSet("wire-skeleton", flag.ContinueOnError)
	flags.IntVar(&options.Clip, "clip", DefaultClip, "replace prose longer than this many bytes")
	flags.Var(&redact, "redact", "replace this literal value wherever it occurs (repeatable)")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: wire-skeleton [-clip N] [-redact VALUE]... INPUT OUTPUT")
		os.Exit(2)
	}
	options.Redact = redact
	if err := run(flags.Arg(0), flags.Arg(1), options); err != nil {
		fmt.Fprintln(os.Stderr, "wire-skeleton:", err)
		os.Exit(1)
	}
}

func run(input, output string, options Options) error {
	source, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var result []byte
	if strings.HasSuffix(input, ".sse") {
		result, err = SkeletonEventStream(source, options)
	} else {
		result, err = Skeleton(source, options)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", input, err)
	}
	return os.WriteFile(output, result, 0o644)
}
