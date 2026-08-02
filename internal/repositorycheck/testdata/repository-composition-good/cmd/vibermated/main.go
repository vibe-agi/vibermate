package main

import "github.com/vibe-agi/vibermate/internal/desktopdaemon"

func main() {
	options, _ := desktopdaemon.ProductionOptions()
	_ = desktopdaemon.Run(options)
}
