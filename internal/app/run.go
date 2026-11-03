// Package app implements the dotfiles CLI: setup wizard, chezmoi hooks, capture and sync.
package app

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	Home, _    = os.UserHomeDir()
	IsMac      = runtime.GOOS == "darwin"
	StateDir   = filepath.Join(Home, ".local/state/dotfiles")
	SecretsDir = filepath.Join(Home, ".config/dotfiles/secrets")

	stepStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// Say prints a progress line.
func Say(format string, a ...any) { fmt.Println(stepStyle.Render("==>"), fmt.Sprintf(format, a...)) }

// Warn prints a warning line.
func Warn(format string, a ...any) { fmt.Println(warnStyle.Render("!!"), fmt.Sprintf(format, a...)) }

// Notify shows a desktop notification (and prints it).
func Notify(title, msg string) {
	Warn("%s: %s", title, msg)
	if IsMac {
		script := fmt.Sprintf("display notification %q with title %q", msg, title)
		_ = exec.Command("osascript", "-e", script).Run()
	} else if Has("notify-send") {
		_ = exec.Command("notify-send", title, msg).Run()
	}
}

// Has reports whether a command is on PATH.
func Has(name string) bool { _, err := exec.LookPath(name); return err == nil }

// Cmd describes a command to run.
type Cmd struct {
	Args  []string
	Dir   string
	Stdin string
	Env   []string // extra KEY=VALUE pairs
}

func (c Cmd) build() *exec.Cmd {
	cmd := exec.Command(c.Args[0], c.Args[1:]...)
	cmd.Dir = c.Dir
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	return cmd
}

// Output runs a command and returns trimmed stdout, or an error including stderr.
func (c Cmd) Output() (string, error) {
	cmd := c.build()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return strings.TrimSpace(out.String()), fmt.Errorf("%s: %w\n%s", strings.Join(c.Args, " "), err, msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// Stream runs a command attached to the terminal.
func (c Cmd) Stream() error {
	cmd := c.build()
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if c.Stdin == "" {
		cmd.Stdin = os.Stdin
	}
	return cmd.Run()
}

// Out is shorthand for Cmd{Args: args}.Output() that ignores errors.
func Out(args ...string) string { s, _ := Cmd{Args: args}.Output(); return s }

// OK reports whether a command exits 0 (output discarded).
func OK(args ...string) bool { return exec.Command(args[0], args[1:]...).Run() == nil }

// Run runs a command and returns an error with stderr on failure.
func Run(args ...string) error { _, err := Cmd{Args: args}.Output(); return err }

// Exists reports whether a path exists.
func Exists(p string) bool { _, err := os.Stat(p); return err == nil }

// WriteIfChanged writes text to path when it differs; reports whether it wrote.
func WriteIfChanged(path, text string) (bool, error) {
	if b, err := os.ReadFile(path); err == nil && string(b) == text {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(text), 0o644)
}

// ExpandHome replaces a leading ~/ with the home directory.
func ExpandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(Home, p[2:])
	}
	return p
}

// Hostname returns the short, lowercase machine name.
func Hostname() string {
	h := ""
	if IsMac {
		h = Out("scutil", "--get", "LocalHostName")
	}
	if h == "" {
		h, _ = os.Hostname()
	}
	return strings.ToLower(strings.SplitN(h, ".", 2)[0])
}

func dim(s string) string { return dimStyle.Render(s) }
