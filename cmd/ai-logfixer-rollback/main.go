package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/CloudSpaceLab/ai-logfixer/internal/agentfix"
)

func main() {
	manifestPath := flag.String("manifest", "", "rollback manifest created by ai-logfixer")
	flag.Parse()

	if *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "-manifest is required")
		os.Exit(2)
	}
	if err := agentfix.Rollback(*manifestPath); err != nil {
		log.Fatalf("rollback failed: %v", err)
	}
	fmt.Fprintln(os.Stderr, "Rollback completed")
}
