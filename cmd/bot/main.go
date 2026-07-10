// Package main is the entry point for the LQBOT QQ robot framework.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Luoyangan/LQBOT/internal/bot"
	"github.com/Luoyangan/LQBOT/internal/version"
)

func main() {
	configPath := flag.String("c", "configs/config.yaml", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		os.Exit(0)
	}

	b, err := bot.NewFromConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nFATAL: failed to create bot: %v\n", err)
		fmt.Fprintf(os.Stderr, "Press Enter to exit...")
		_, _ = fmt.Scanln()
		os.Exit(1)
	}

	// Run blocks until SIGINT/SIGTERM or startup failure
	if err := b.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nFATAL: bot startup failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "Press Enter to exit...")
		_, _ = fmt.Scanln()
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "\nBot stopped. Press Enter to exit...")
	_, _ = fmt.Scanln()

	// Ensure process exits (some goroutines may block on system calls)
	os.Exit(0)
}
