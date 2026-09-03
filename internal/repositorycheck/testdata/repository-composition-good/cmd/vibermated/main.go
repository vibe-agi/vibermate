package main

import (
	"github.com/vibe-agi/vibermate/internal/desktopdaemon"
	"github.com/vibe-agi/vibermate/internal/serverdaemon"
)

func main() {
	options, _ := desktopdaemon.ProductionOptions()
	_ = desktopdaemon.Run(options)
}

func runServer() {
	options, _ := serverdaemon.ProductionOptions()
	_ = serverdaemon.Run(options)
}
