package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/lyrka-meow/Regalia/internal/daemon"
	"github.com/lyrka-meow/Regalia/internal/engine"
	"github.com/lyrka-meow/Regalia/internal/paths"
	"github.com/lyrka-meow/Regalia/internal/state"
)

func main() {
	socketPath := flag.String("socket", paths.Socket(), "Unix socket path")
	engineBinary := flag.String("engine", "sing-box", "sing-box executable path")
	engineConfig := flag.String("engine-config", paths.EngineConfig(), "temporary sing-box configuration path")
	engineLog := flag.String("engine-log", paths.EngineLog(), "sing-box log path")
	statePath, err := paths.State()
	if err != nil {
		log.Fatal(err)
	}
	stateFile := flag.String("state", statePath, "persistent state file")
	flag.Parse()

	store, err := state.Open(*stateFile)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "regaliad: listening on %s\n", *socketPath)
	controller := engine.NewProcess(*engineBinary, *engineConfig, *engineLog)
	if err := daemon.NewWithEngine(*socketPath, store, controller).ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
