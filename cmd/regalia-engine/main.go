package main

import (
	"flag"
	"log"

	"github.com/lyrka-meow/Regalia/internal/enginebridge"
)

func main() {
	configPath := flag.String("config", "", "Regalia-generated configuration path")
	binaryPath := flag.String("binary", "/usr/lib/regalia/sing-box", "root-owned sing-box executable path")
	logPath := flag.String("log", "", "private sing-box log path")
	flag.Parse()
	if *configPath == "" || *logPath == "" {
		log.Fatal("-config and -log are required")
	}
	if err := enginebridge.Run(*configPath, *binaryPath, *logPath); err != nil {
		log.Fatal(err)
	}
}
