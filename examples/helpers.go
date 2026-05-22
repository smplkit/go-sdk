//go:build ignore

package main

import (
	"fmt"
	"os"
)

// fatalIfErr prints err and exits with status 1.
func fatalIfErr(msg string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", msg, err)
		os.Exit(1)
	}
}
