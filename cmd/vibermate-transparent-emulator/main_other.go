//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "vibermate-transparent-emulator runs on Linux only")
	os.Exit(2)
}
