package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/systray"
)

// Trays built on libdbusmenu, snixembed among them, can drop a submenu's
// items when they arrive right after the submenu itself.
const subMenuDelay = 10 * time.Millisecond

// Linux trays show the StatusNotifierItem title as the tooltip.
func setTooltip(s string) { systray.SetTitle(s) }

func copyText(s string) error {
	var cmd *exec.Cmd
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		cmd = exec.Command("wl-copy")
	case has("xclip"):
		cmd = exec.Command("xclip", "-selection", "clipboard")
	default:
		cmd = exec.Command("xsel", "--clipboard", "--input")
	}
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

func has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func openURL(u string) error { return exec.Command("xdg-open", u).Run() }

// openTerminal runs args in a new Ghostty window, which outlives the call;
// Wait only reaps it once the window closes.
func openTerminal(args ...string) error {
	cmd := exec.Command("ghostty", append([]string{"-e"}, args...)...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func downloads() (string, error) {
	if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil {
		if dir := strings.TrimSpace(string(out)); dir != "" {
			return dir, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
}

func listeners() ([]listener, error) {
	out, err := exec.Command("ss", "-Hltnp").Output()
	if err != nil {
		return nil, fmt.Errorf("listing listening ports: %w", err)
	}
	return interactive(parseSS(string(out))), nil
}

func notify(title, body string) {
	if err := exec.Command("notify-send", "--app-name=Tailtray", title, body).Run(); err != nil {
		log.Printf("notifying %q: %v", title, err)
	}
}

func pickFile(prompt string) (string, error) {
	return zenity("--file-selection", "--title="+prompt)
}

func askText(prompt, answer string) (string, bool, error) {
	s, err := zenity("--entry", "--title=Tailtray", "--text="+prompt, "--entry-text="+answer)
	if err != nil {
		return "", false, err
	}
	return s, s != "", nil
}

func confirm(prompt, ok string) bool {
	_, err := exec.Command("zenity", "--question", "--title=Tailtray", "--text="+prompt, "--ok-label="+ok).Output()
	return err == nil
}

// zenity exits 1 when the dialog is canceled, which returns "" and no error.
func zenity(args ...string) (string, error) {
	out, err := exec.Command("zenity", args...).Output()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.ExitCode() == 1 {
		return "", nil
	}
	return strings.TrimSuffix(string(out), "\n"), err
}
