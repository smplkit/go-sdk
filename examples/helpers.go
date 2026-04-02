//go:build ignore

package main

import (
	"fmt"
	"os"
)

func section(title string) {
	fmt.Printf("\n%s\n", "════════════════════════════════════════════════════════════════")
	fmt.Printf("  %s\n", title)
	fmt.Printf("%s\n\n", "════════════════════════════════════════════════════════════════")
}

func step(description string) {
	fmt.Printf("  → %s\n", description)
}

func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", msg, err)
	os.Exit(1)
}

func strPtr(s string) *string { return &s }
