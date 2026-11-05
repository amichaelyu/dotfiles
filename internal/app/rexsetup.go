package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
)

// `dotfiles rex-setup` (or "Rex + Tailscale only" in setup): make this machine reachable from Rex,
// with nothing else installed. Rex joins the tailnet itself as its own device (Tailscale built into
// Rex), named NAME-rex and tagged tag:rex like your other hosts, so the machine needs no Tailscale
// install. Linux runs the server as a systemd user service; macOS lets the Rex app run it.

const rexTag = "tag:rex"

var macRexBin = filepath.Join(Home, "Library/Application Support/com.superlogical.rex/RexServer/Versions/Current/Rex Server.app/Contents/MacOS/rex")

func rexBinary() string {
	if IsMac {
		if Exists(macRexBin) {
			return macRexBin
		}
		return ""
	}
	for _, p := range []string{"/usr/bin/rex", "/usr/local/bin/rex", filepath.Join(Home, ".local/bin/rex")} {
		if Exists(p) {
			return p
		}
	}
	return ""
}

// CmdRexSetup installs Rex, puts its server on the tailnet, and keeps it running.
func CmdRexSetup() error {
	fmt.Println(titleStyle.Render("Rex + Tailscale"))
	name := defaultRexNodeName()
	if err := huh.NewInput().Title("Tailnet name for this machine's Rex").
		Description("Your other hosts follow the same pattern, like mac-mini-rex.").Value(&name).Run(); err != nil {
		return err
	}
	name = sanitizeHostname(name)

	rex, err := ensureRex()
	if err != nil {
		return err
	}
	if IsMac {
		err = startRexMac(rex, name)
	} else {
		err = startRexLinux(rex, name)
	}
	if err != nil {
		return err
	}
	url, err := waitRexOnTailnet(rex)
	if err != nil {
		return err
	}
	label := strings.TrimSuffix(name, "-rex")
	fmt.Println()
	Say("Rex is on your tailnet at %s", url)
	Say("to add it to your Rex hosts, run this on your main Mac:")
	fmt.Printf("\n    rex hosts add %s %s\n\n", shq(label), url)
	return nil
}

func defaultRexNodeName() string {
	h := Out("hostname", "-s")
	if IsMac {
		if c := Out("scutil", "--get", "ComputerName"); c != "" {
			h = c
		}
	}
	return sanitizeHostname(h) + "-rex"
}

var hostnameJunk = regexp.MustCompile(`[^a-z0-9-]+`)

func sanitizeHostname(s string) string {
	s = hostnameJunk.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "rex"
	}
	return s
}

// ensureRex installs Rex if it's missing and returns the rex binary.
func ensureRex() (string, error) {
	if b := rexBinary(); b != "" {
		return b, nil
	}
	if IsMac {
		app := rexApp()
		if app == "" {
			Say("Rex for Mac is a beta download (sign in with Discord); opening the download page")
			_ = openURL("https://www.superlogical.com/rex/docs/installation")
			Say("waiting for Rex to appear in /Applications...")
			for app == "" {
				time.Sleep(3 * time.Second)
				app = rexApp()
			}
		}
		Say("opening %s once so it installs its server", filepath.Base(app))
		_ = Run("open", "-g", app)
		for i := 0; i < 60 && !Exists(macRexBin); i++ {
			time.Sleep(2 * time.Second)
		}
		if !Exists(macRexBin) {
			return "", fmt.Errorf("Rex didn't install its server; open %s and try again", filepath.Base(app))
		}
		return macRexBin, nil
	}
	Say("installing Rex from pkgs.superlogical.com (asks for your sudo password)")
	if err := (Cmd{Args: []string{"sh", "-c", "curl -fsSL https://pkgs.superlogical.com/rex/install.sh | sh"}}).Stream(); err != nil {
		return "", fmt.Errorf("Rex install failed (it needs sudo): %w", err)
	}
	if b := rexBinary(); b != "" {
		return b, nil
	}
	return "", fmt.Errorf("Rex installed, but no rex binary found")
}

func rexApp() string {
	m, _ := filepath.Glob("/Applications/Rex*.app")
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

// startRexMac: the Rex app runs the server and turns on its tailnet node from these settings
// (what the app's own Tailscale switch stores), and opens at login.
func startRexMac(rex, name string) error {
	app := rexApp()
	agent := filepath.Join(Home, "Library/LaunchAgents/com.michaelyu.rex-at-login.plist")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.michaelyu.rex-at-login</string>
  <key>ProgramArguments</key>
  <array><string>/usr/bin/open</string><string>-g</string><string>%s</string></array>
  <key>RunAtLoad</key><true/>
</dict>
</plist>
`, app)
	if _, err := WriteIfChanged(agent, plist); err != nil {
		return err
	}
	_ = Run("open", "-g", app)
	for i := 0; i < 30 && !OK(rex, "server", "status"); i++ {
		time.Sleep(time.Second)
	}
	// Enroll first, so the name and tag apply (only a node's first sign-in takes them; if the app's
	// setting were already on, the app would enroll the node itself, untagged). Then turn the
	// setting on so the app brings the node back after restarts.
	if err := (Cmd{Args: []string{rex, "server", "tailscale", "on", "--hostname", name, "--advertise-tag", rexTag, "--allow-tailnet-members"}}).Stream(); err != nil {
		return err
	}
	_ = Run("defaults", "write", "com.superlogical.rex", "rex.server.tailnet-enabled", "-bool", "true")
	_ = Run("defaults", "write", "com.superlogical.rex", "rex.server.tailnet-allow-tailnet-members", "-bool", "true")
	return nil
}

// startRexLinux runs `rex server run --tailscale ...` as a systemd user service that survives
// reboots (lingering), replacing any personal server a rex command started.
func startRexLinux(rex, name string) error {
	args := []string{rex, "server", "run", "--tailscale", "--tailscale-hostname", name,
		"--tailscale-advertise-tag", rexTag, "--tailscale-allow-tailnet-members"}
	if !OK("systemctl", "--user", "show-environment") {
		Warn("no systemd user session here; starting Rex now, but it won't come back after a reboot")
		_ = Run(rex, "server", "stop")
		start := append([]string{rex, "server", "start"}, args[3:]...)
		return Cmd{Args: start}.Stream()
	}
	unit := fmt.Sprintf(`[Unit]
Description=Rex server on the tailnet (dotfiles rex-setup)

[Service]
ExecStart=%s
Restart=on-failure

[Install]
WantedBy=default.target
`, strings.Join(args, " "))
	path := filepath.Join(Home, ".config/systemd/user/rex-tailnet.service")
	if _, err := WriteIfChanged(path, unit); err != nil {
		return err
	}
	_ = Run(rex, "server", "stop") // a personal server a rex command started holds the socket
	if !OK("loginctl", "enable-linger", os.Getenv("USER")) {
		Warn("couldn't enable lingering; Rex runs while you're logged in (ask an admin for `loginctl enable-linger %s`)", os.Getenv("USER"))
	}
	_ = Run("systemctl", "--user", "daemon-reload")
	return Cmd{Args: []string{"systemctl", "--user", "enable", "--now", "rex-tailnet.service"}}.Stream()
}

// waitRexOnTailnet shows the Tailscale sign-in link if the node needs one, and waits until it's up.
func waitRexOnTailnet(rex string) (string, error) {
	shown := false
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		var st struct {
			Tailscale struct {
				State string `json:"state"`
				HTTPS struct {
					URL string `json:"url"`
				} `json:"https"`
			} `json:"tailscale"`
		}
		_ = json.Unmarshal([]byte(Out(rex, "server", "status", "--json")), &st)
		if st.Tailscale.State == "running" && st.Tailscale.HTTPS.URL != "" {
			return st.Tailscale.HTTPS.URL, nil
		}
		if !shown {
			if link := findLoginURL(Out(rex, "server", "tailscale", "login", "--json")); link != "" {
				Say("sign in to Tailscale to add this machine to your tailnet (any device works):")
				fmt.Printf("\n    %s\n\n", link)
				_ = openURL(link)
				Say("waiting for the sign-in...")
				shown = true
			}
		}
		time.Sleep(3 * time.Second)
	}
	return "", fmt.Errorf("Rex didn't come up on the tailnet; check `rex server status`")
}

var tsLoginRe = regexp.MustCompile(`https://login\.tailscale\.com/[^"\s]+`)

func findLoginURL(s string) string { return tsLoginRe.FindString(s) }
