package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"fyne.io/systray"
)

const subMenuDelay = 0

// On macOS the systray title is text beside the icon, so the tooltip has
// its own call.
func setTooltip(s string) { systray.SetTooltip(s) }

func copyText(s string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

func openURL(u string) error { return exec.Command("open", u).Run() }

// openTerminal runs args in a new Ghostty window.
func openTerminal(args ...string) error {
	return exec.Command("open", append([]string{"-na", "Ghostty", "--args", "-e"}, args...)...).Run()
}

func downloads() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
}

// listeners returns the ports this user's terminal processes listen on;
// without root, lsof sees only this user's processes.
func listeners() ([]listener, error) {
	out, err := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpcn").Output()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.ExitCode() == 1 && len(out) == 0 {
		return nil, nil // lsof exits 1 when nothing listens
	}
	if err != nil {
		return nil, fmt.Errorf("listing listening ports: %w", err)
	}
	return interactive(parseLsof(string(out))), nil
}

func notify(title, body string) {
	if _, err := osascript("display notification (item 2 of argv) with title (item 1 of argv)", title, body); err != nil {
		log.Printf("notifying %q: %v", title, err)
	}
}

func pickFile(prompt string) (string, error) {
	return osascript("return POSIX path of (choose file with prompt (item 1 of argv))", prompt)
}

func askText(prompt, answer string) (string, bool, error) {
	s, err := osascript(`return text returned of (display dialog (item 1 of argv) default answer (item 2 of argv) buttons {"Cancel", "OK"} default button "OK")`, prompt, answer)
	if err != nil {
		return "", false, err
	}
	return s, s != "", nil
}

func confirm(prompt, ok string) bool {
	s, err := osascript(`return button returned of (display dialog (item 1 of argv) buttons {"Cancel", (item 2 of argv)} default button "Cancel" with icon caution)`, prompt, ok)
	return err == nil && s == ok
}

// osascript runs statement with the arguments in argv, so no text is ever
// spliced into the script. A press of Cancel returns "" and no error.
func osascript(statement string, args ...string) (string, error) {
	cmdArgs := append([]string{"-e", "on run argv", "-e", statement, "-e", "end run", "--"}, args...)
	out, err := exec.Command("osascript", cmdArgs...).Output()
	// -128 is AppleScript's user-canceled error, which osascript reports
	// only as text on stderr.
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && strings.Contains(string(ee.Stderr), "(-128)") {
		return "", nil
	}
	return strings.TrimSuffix(string(out), "\n"), err
}
