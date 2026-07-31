package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/lyrka-meow/Regalia/internal/daemon"
	"github.com/lyrka-meow/Regalia/internal/paths"
	"github.com/lyrka-meow/Regalia/internal/state"
)

func main() {
	socketPath := flag.String("socket", paths.Socket(), "Unix socket path")
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
	if err := daemon.New(*socketPath, store).ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
