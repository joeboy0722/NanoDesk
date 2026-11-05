//go:build !windows

package main

import "fmt"

func runServerGUI() error {
	return fmt.Errorf("GUI is available on Windows; use -cli for command-line mode")
}
func startupError(err error) { cliError(err) }

func prepareCLI() {}
