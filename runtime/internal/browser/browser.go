// Package browser opens the system browser at a URL.
//
// It exists as its own package because opening a browser is the one part of the
// startup path that cannot be verified by a test: the Runtime hands over the
// session credential by opening the client itself, so the intent is isolated
// here where it is easy to see and easy to skip.
package browser

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Open asks the operating system to open target in the default browser. It
// returns as soon as the launcher has been started: the browser is a separate,
// long-lived process and the Runtime does not wait for it.
func Open(target string) error {
	command, err := command(target)
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("browser: start launcher: %w", err)
	}
	// The launcher is not waited for, so its resources must be released here
	// rather than left to a reaper the Runtime does not have.
	_ = command.Process.Release()
	return nil
}

func command(target string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "windows":
		// rundll32 is the launcher that needs no shell, so the URL never passes
		// through command-line parsing that could reinterpret it.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target), nil
	case "darwin":
		return exec.Command("open", target), nil
	case "linux":
		return exec.Command("xdg-open", target), nil
	default:
		return nil, fmt.Errorf("browser: no launcher is known for %s", runtime.GOOS)
	}
}
