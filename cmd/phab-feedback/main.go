package main

import (
	"os"

	"github.com/loganrosen/phab-feedback/internal/phabfeedback"
)

func main() {
	os.Exit(phabfeedback.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
