package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// `dotfiles login`: get every account on this machine signed in.
//
//   auto     signed in from a Bitwarden secret or by the tool's own CLI (Tailscale, gh, Codex)
//   browser  the app opens a sign-in page; Zen is already signed in (zen-push), and the page's
//            "Continue" / "Open <App>" button is clicked automatically (falls back to you)
//   qr       scan a QR code with your phone (Discord, Steam)
//   manual   needs you (Office + Duo, Beeper recovery key, Apple ID, ...)

type signedIn int

const (
	unknown signedIn = iota
	yes
	no
)

func (s signedIn) String() string { return [...]string{"?", "✓", "✗"}[s] }

type recipe struct {
	URL     *regexp.Regexp // sign-in page(s) to act on
	Click   []string       // button labels, tried in order (exact text, case-insensitive)
	Require string         // only click if the page mentions this (guards OAuth consent pages)
}

type loginApp struct {
	ID, Name string
	App      string // /Applications bundle that must exist ("" = CLI tool, see Bin)
	Bin      string // CLI that must exist
	Kind     string // auto | browser | qr | manual
	Optional bool   // only offered after `dotfiles login --add ID`
	Check    func(d *Data) signedIn
	Start    func(d *Data) error // kick off sign-in
	Hint     string
	Schemes  []string // URL schemes the sign-in page hands back to the app
	Recipes  []recipe
}

func appPath(name string) string { return filepath.Join("/Applications", name+".app") }
func openApp(name string) func(*Data) error {
	return func(*Data) error { return Run("open", "-a", name) }
}
func fileHas(path, needle string) bool {
	b, err := os.ReadFile(ExpandHome(path))
	return err == nil && strings.Contains(string(b), needle)
}
func boolCheck(ok bool) signedIn {
	if ok {
		return yes
	}
	return no
}
func secretFile(name string) string {
	b, err := os.ReadFile(filepath.Join(SecretsDir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
func identity(d *Data, key string) string { s, _ := d.Identity[key].(string); return s }

func tailscaleBin() string {
	for _, p := range []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale", "/usr/local/bin/tailscale"} {
		if Exists(p) {
			return p
		}
	}
	return "tailscale"
}

var loginApps = []loginApp{
	// ------------------------------------------------------------------ automatic
	{ID: "tailscale", Name: "Tailscale", Bin: "tailscale", Kind: "auto",
		Check: func(*Data) signedIn {
			var st struct{ BackendState string }
			_ = json.Unmarshal([]byte(Out(tailscaleBin(), "status", "--json")), &st)
			return boolCheck(st.BackendState == "Running")
		},
		Start: func(d *Data) error {
			ts := tailscaleBin()
			if key := secretFile("tailscale-oauth"); key != "" && !IsMac { // servers: tagged, never expires
				tags := identity(d, "tailscale_tags")
				if tags == "" {
					tags = "tag:server"
				}
				return Cmd{Args: []string{"sudo", ts, "up", "--auth-key=" + key + "?ephemeral=false", "--advertise-tags=" + tags}}.Stream()
			}
			if key := secretFile("tailscale-authkey"); key != "" {
				return Cmd{Args: []string{ts, "up", "--auth-key=" + key}}.Stream()
			}
			return Cmd{Args: []string{ts, "up"}}.Stream() // prints a login URL; the browser step clicks Connect
		},
		Schemes: nil, Recipes: []recipe{{URL: regexp.MustCompile(`login\.tailscale\.com/a/`), Click: []string{"Connect"}}}},
	{ID: "gh", Name: "GitHub CLI", Bin: "gh", Kind: "auto",
		Check: func(*Data) signedIn { return boolCheck(OK("gh", "auth", "status")) },
		Start: func(*Data) error {
			if tok := secretFile("gh-token"); tok != "" {
				return Cmd{Args: []string{"gh", "auth", "login", "--with-token"}, Stdin: tok + "\n"}.Stream()
			}
			return ensureGhAuth()
		}},
	{ID: "claude", Name: "Claude Code", Bin: "claude", Kind: "auto",
		Check: func(*Data) signedIn {
			return boolCheck(strings.Contains(Out("claude", "auth", "status"), `"loggedIn": true`) || os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "")
		},
		Hint: "store a `claude setup-token` token as `claude-oauth-token` in your secrets, or run `claude` and use /login"},
	{ID: "codex", Name: "Codex CLI", Bin: "codex", Kind: "auto",
		Check: func(*Data) signedIn {
			out, _ := exec.Command("codex", "login", "status").CombinedOutput()
			return boolCheck(strings.Contains(string(out), "Logged in"))
		},
		Start:   func(*Data) error { return Cmd{Args: []string{"codex", "login"}}.Stream() },
		Recipes: []recipe{{URL: regexp.MustCompile(`auth\.openai\.com/`), Click: []string{"Continue", "Allow"}, Require: "Codex"}}},
	{ID: "gcloud", Name: "Google Cloud CLI", Bin: "gcloud", Kind: "auto",
		Check: func(*Data) signedIn {
			return boolCheck(Out("gcloud", "auth", "list", "--filter=status:ACTIVE", "--format=value(account)") != "")
		},
		Start:   func(*Data) error { return Cmd{Args: []string{"gcloud", "auth", "login"}}.Stream() },
		Recipes: []recipe{{URL: regexp.MustCompile(`accounts\.google\.com/.*(oauth|signin/oauth)`), Click: []string{"Continue", "Allow"}, Require: "Google Cloud SDK"}}},

	// ------------------------------------------------------------------ browser (auto-clicked)
	{ID: "slack", Name: "Slack", App: "Slack", Kind: "browser",
		Check: func(*Data) signedIn {
			var st struct {
				Workspaces map[string]any `json:"workspaces"`
			}
			b, err := os.ReadFile(ExpandHome("~/Library/Application Support/Slack/storage/root-state.json"))
			if err != nil || json.Unmarshal(b, &st) != nil {
				return no
			}
			return boolCheck(len(st.Workspaces) > 0)
		},
		Start: openApp("Slack"), Hint: "in Slack, click Sign In (it opens Zen)", Schemes: []string{"slack"},
		Recipes: []recipe{{URL: regexp.MustCompile(`slack\.com/(ssb|signin|workspace-signin|get-started|sign_in)`), Click: []string{"Open Slack", "Open in Slack", "Launch Slack", "Continue", "Sign in to Slack"}}}},
	{ID: "figma", Name: "Figma", App: "Figma", Kind: "browser",
		Start: openApp("Figma"), Hint: "in Figma, click Log in with browser", Schemes: []string{"figma"},
		Recipes: []recipe{{URL: regexp.MustCompile(`figma\.com/(app_auth|login|desktop|oauth)`), Click: []string{"Open Figma", "Open in Figma", "Open Figma app", "Continue", "Log in to Figma app"}}}},
	{ID: "zed", Name: "Zed", App: "Zed", Kind: "browser",
		Check: func(*Data) signedIn {
			return boolCheck(OK("security", "find-internet-password", "-s", "https://zed.dev"))
		},
		Start: openApp("Zed"), Hint: "in Zed, click Sign In (top right)", Schemes: []string{"zed"},
		Recipes: []recipe{
			{URL: regexp.MustCompile(`zed\.dev/`), Click: []string{"Connect", "Sign in", "Continue"}},
			{URL: regexp.MustCompile(`github\.com/login/oauth/authorize`), Click: []string{"Authorize zed-industries", "Authorize"}, Require: "Zed"},
		}},
	{ID: "raycast", Name: "Raycast", App: "Raycast", Kind: "browser",
		Start: openApp("Raycast"), Hint: "in Raycast Settings → Account, click Log In", Schemes: []string{"raycast", "com.raycast"},
		Recipes: []recipe{{URL: regexp.MustCompile(`raycast\.com/`), Click: []string{"Open Raycast", "Continue", "Authorize"}}}},
	{ID: "zoom", Name: "Zoom", App: "zoom.us", Kind: "browser",
		Check: func(*Data) signedIn {
			m, _ := filepath.Glob(ExpandHome("~/Library/Application Support/zoom.us/data/*@xmpp.zoom.us"))
			return boolCheck(len(m) > 0)
		},
		Start: func(d *Data) error {
			if dom := identity(d, "zoom_sso_domain"); dom != "" { // SSO straight from the browser
				return Run("open", "-a", "Zen", fmt.Sprintf("https://%s.zoom.us/saml/login?from=desktop", dom))
			}
			return Run("open", "-a", "zoom.us")
		},
		Hint: "approve Duo if asked", Schemes: []string{"zoommtg", "zoomus"},
		Recipes: []recipe{{URL: regexp.MustCompile(`zoom\.us/`), Click: []string{"Launch Zoom", "Open Zoom", "Open zoom.us", "Open link"}}}},

	// ------------------------------------------------------------------ phone QR
	{ID: "discord", Name: "Discord", App: "Discord", Kind: "qr", Start: openApp("Discord"),
		Hint: "scan the QR code with the Discord app on your phone (Settings → Scan QR Code)"},
	{ID: "steam", Name: "Steam", App: "Steam", Kind: "qr",
		Check: func(*Data) signedIn {
			return boolCheck(fileHas("~/Library/Application Support/Steam/config/loginusers.vdf", `"RememberPassword"		"1"`))
		},
		Start: openApp("Steam"), Hint: "scan the QR code with the Steam app on your phone (Steam Guard → QR)"},

	// ------------------------------------------------------------------ manual
	{ID: "bitwarden", Name: "Bitwarden", App: "Bitwarden", Kind: "manual", Start: openApp("Bitwarden"),
		Hint: "log in, enable Touch ID unlock, and turn on the SSH agent (Settings → SSH agent)"},
	{ID: "office", Name: "Microsoft Office", App: "Microsoft Word", Kind: "manual",
		Check: func(*Data) signedIn {
			return boolCheck(Exists(ExpandHome("~/Library/Group Containers/UBF8T346G9.com.microsoft.oneauth")))
		},
		Start: openApp("Microsoft Word"), Hint: "sign in once with your work account (+ Duo); Excel, PowerPoint and Outlook share it"},
	{ID: "beeper", Name: "Beeper", App: "Beeper Desktop", Kind: "manual", Start: openApp("Beeper Desktop"),
		Hint: "sign in with the emailed code, then enter your recovery key (in Bitwarden) to unlock encrypted chats"},
	{ID: "chatgpt", Name: "ChatGPT", App: "ChatGPT", Kind: "manual", Start: openApp("ChatGPT"), Hint: "sign in"},
	{ID: "ticktick", Name: "TickTick", App: "TickTick", Kind: "manual", Start: openApp("TickTick"), Hint: "sign in"},
	{ID: "spotifast", Name: "Spotifast", App: "Spotifast", Kind: "manual", Start: openApp("Spotifast"), Hint: "sign in to Spotify"},
	{ID: "lunar", Name: "Lunar Client", App: "Lunar Client", Kind: "manual", Start: openApp("Lunar Client"), Hint: "add your Microsoft (Minecraft) account"},
	{ID: "modrinth", Name: "Modrinth", App: "Modrinth App", Kind: "manual", Start: openApp("Modrinth App"), Hint: "sign in with your Microsoft (Minecraft) account"},
	{ID: "obsidian", Name: "Obsidian (LiveSync)", App: "Obsidian", Kind: "manual", Start: openApp("Obsidian"),
		Hint: "open each vault; paste its LiveSync Setup URI (in Bitwarden) if it isn't syncing"},
	{ID: "appleid", Name: "Apple ID (App Store, iCloud, Xcode)", Kind: "manual", Optional: false,
		Start: func(*Data) error {
			return Run("open", "x-apple.systempreferences:com.apple.systempreferences.AppleIDSettings")
		},
		Hint: "sign in to your Apple ID; for Xcode also Xcode → Settings → Accounts"},
	{ID: "logi", Name: "Logi Options+", App: "logioptionsplus", Kind: "manual", Optional: true, Start: openApp("logioptionsplus"), Hint: "sign in to restore your mouse/keyboard settings backup"},
	{ID: "bambu", Name: "Bambu Studio", App: "BambuStudio", Kind: "manual", Optional: true, Start: openApp("BambuStudio"), Hint: "sign in for cloud printing"},
	{ID: "ollama", Name: "Ollama", Bin: "ollama", Kind: "manual", Optional: true, Start: func(*Data) error { return Cmd{Args: []string{"ollama", "signin"}}.Stream() }, Hint: "for cloud models"},
}

// usesBrowser: browser-kind apps, and CLI logins that print a URL (unless a secret makes it unattended).
func (a loginApp) usesBrowser() bool {
	if a.Kind == "browser" {
		return true
	}
	if a.Kind != "auto" || len(a.Recipes) == 0 || !IsMac {
		return false
	}
	if a.ID == "tailscale" && (secretFile("tailscale-authkey") != "" || secretFile("tailscale-oauth") != "") {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------- state

type loginState struct {
	Done    map[string]string `json:"done"`    // id -> date marked done (for apps we can't detect)
	Skipped map[string]bool   `json:"skipped"` // never ask on this machine
	Wanted  map[string]bool   `json:"wanted"`  // optional apps opted in
}

var loginStatePath = filepath.Join(StateDir, "login.json")

func readLoginState() loginState {
	st := loginState{Done: map[string]string{}, Skipped: map[string]bool{}, Wanted: map[string]bool{}}
	_ = readJSON(loginStatePath, &st)
	if st.Done == nil {
		st.Done = map[string]string{}
	}
	if st.Skipped == nil {
		st.Skipped = map[string]bool{}
	}
	if st.Wanted == nil {
		st.Wanted = map[string]bool{}
	}
	return st
}

func (a loginApp) installed() bool {
	if a.App != "" {
		return Exists(appPath(a.App))
	}
	if a.Bin != "" {
		return Has(a.Bin) || (a.Bin == "tailscale" && tailscaleBin() != "tailscale")
	}
	return IsMac
}

func (a loginApp) status(d *Data, st loginState) signedIn {
	if a.Check != nil {
		return a.Check(d)
	}
	if st.Done[a.ID] != "" {
		return yes
	}
	return unknown
}

// relevant apps for this machine: installed, not excluded, not skipped, optional only when wanted.
func relevantApps(d *Data, st loginState) []loginApp {
	var out []loginApp
	for _, a := range loginApps {
		if !a.installed() || d.Excluded(a.ID) || d.Excluded(a.Name) || (a.App != "" && d.Excluded(a.App)) || st.Skipped[a.ID] {
			continue
		}
		if a.Optional && !st.Wanted[a.ID] {
			continue
		}
		if !IsMac && a.App != "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// ---------------------------------------------------------------------------- command

// CmdLogin: (no args) sign in to everything pending | --status | --add ID | --skip ID | --reset ID | ID...
func CmdLogin(args []string) error {
	d, err := LoadData()
	if err != nil {
		return err
	}
	st := readLoginState()
	if len(args) > 0 {
		switch args[0] {
		case "--status", "status":
			printLoginStatus(d, st)
			return nil
		case "--add", "--skip", "--reset":
			if len(args) < 2 {
				return fmt.Errorf("usage: dotfiles login %s ID", args[0])
			}
			for _, id := range args[1:] {
				switch args[0] {
				case "--add":
					st.Wanted[id] = true
					delete(st.Skipped, id)
				case "--skip":
					st.Skipped[id] = true
				case "--reset":
					delete(st.Done, id)
					delete(st.Skipped, id)
				}
			}
			return writeJSON(loginStatePath, st)
		}
	}
	only := set(args)
	var pending []loginApp
	for _, a := range relevantApps(d, st) {
		if len(only) > 0 && !only[a.ID] {
			continue
		}
		if a.status(d, st) != yes {
			pending = append(pending, a)
		}
	}
	if len(pending) == 0 {
		Say("everything is signed in")
		return nil
	}
	in := bufio.NewReader(os.Stdin)
	byKind := func(k string) []loginApp {
		var out []loginApp
		for _, a := range pending {
			if a.Kind == k {
				out = append(out, a)
			}
		}
		return out
	}

	// 1. automatic
	for _, a := range byKind("auto") {
		if a.usesBrowser() {
			continue // handled with the browser group
		}
		if a.Start == nil {
			Warn("%s: %s", a.Name, a.Hint)
			continue
		}
		Say("signing in: %s", a.Name)
		if err := a.Start(d); err != nil {
			Warn("%s: %v", a.Name, firstLine(err.Error()))
		}
	}

	// 2. browser flows (Zen under remote control clicks the buttons)
	var browserApps []loginApp
	for _, a := range pending {
		if a.usesBrowser() {
			browserApps = append(browserApps, a)
		}
	}
	if len(browserApps) > 0 && IsMac {
		if err := runBrowserLogins(d, &st, browserApps, in); err != nil {
			Warn("browser sign-ins: %v", err)
		}
	}

	// 3. phone QR + 4. manual: open each, wait for you
	for _, a := range append(byKind("qr"), byKind("manual")...) {
		fmt.Printf("\n%s %s: %s\n", stepStyle.Render("→"), a.Name, a.Hint)
		if a.Start != nil {
			_ = a.Start(d)
		}
		fmt.Print(dim("  press Enter when signed in, s to skip on this machine, n for not now: "))
		ans, _ := in.ReadString('\n')
		switch strings.TrimSpace(strings.ToLower(ans)) {
		case "s":
			st.Skipped[a.ID] = true
		case "n":
		default:
			st.Done[a.ID] = time.Now().Format("2006-01-02")
		}
		_ = writeJSON(loginStatePath, st)
	}
	fmt.Println()
	printLoginStatus(d, st)
	return nil
}

func runBrowserLogins(d *Data, st *loginState, apps []loginApp, in *bufio.Reader) error {
	var schemes []string
	for _, a := range apps {
		schemes = append(schemes, a.Schemes...)
	}
	sess, b, err := startZenSession(schemes)
	if err != nil {
		return err
	}
	defer func() {
		b.close()
		sess.Restore()
	}()
	Say("Zen is under remote control; sign-in pages are clicked through automatically")

	for _, a := range apps {
		fmt.Printf("\n%s %s", stepStyle.Render("→"), a.Name)
		if a.Hint != "" {
			fmt.Printf(": %s", a.Hint)
		}
		fmt.Println()
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); autoClick(b, a.Recipes, stop) }()
		if a.Start != nil {
			// CLI logins (tailscale up, codex login, gcloud auth login) block until the browser side
			// finishes; app launches return immediately.
			if err := a.Start(d); err != nil {
				Warn("%s: %v", a.Name, firstLine(err.Error()))
			}
		}
		result := unknown
		if a.Check != nil {
			result = waitSignedIn(d, a, 3*time.Minute)
		}
		if result != yes {
			if a.Check != nil {
				fmt.Print(dim("  couldn't confirm automatically. "))
			}
			fmt.Print(dim("Enter = signed in, s = skip on this machine, n = not now: "))
			ans, _ := in.ReadString('\n')
			switch strings.TrimSpace(strings.ToLower(ans)) {
			case "s":
				st.Skipped[a.ID] = true
			case "n":
			default:
				st.Done[a.ID] = time.Now().Format("2006-01-02")
			}
			_ = writeJSON(loginStatePath, *st)
		} else {
			Say("%s signed in", a.Name)
		}
		close(stop)
		wg.Wait()
	}
	return nil
}

// autoClick watches Zen's tabs and clicks the matching button on recognized sign-in pages.
func autoClick(b *bidi, recipes []recipe, stop chan struct{}) {
	clicked := map[string]bool{}
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		tabs, err := b.tabs()
		if err != nil {
			return
		}
		for _, t := range tabs {
			for _, r := range recipes {
				key := t.Context + "|" + t.URL
				if clicked[key] || !r.URL.MatchString(t.URL) {
					continue
				}
				if label, err := b.clickByText(t.Context, r.Click, r.Require); err == nil && label != "" {
					clicked[key] = true
					Say("clicked %q on %s", label, hostOf(t.URL))
				}
			}
		}
	}
}

// waitSignedIn polls the app's own signed-in check until it passes or times out.
func waitSignedIn(d *Data, a loginApp, timeout time.Duration) signedIn {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if a.Check(d) == yes {
			return yes
		}
		time.Sleep(2 * time.Second)
	}
	return no
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.SplitN(u, "/", 2)[0]
}

func printLoginStatus(d *Data, st loginState) {
	apps := relevantApps(d, st)
	sort.SliceStable(apps, func(i, j int) bool {
		order := map[string]int{"auto": 0, "browser": 1, "qr": 2, "manual": 3}
		return order[apps[i].Kind] < order[apps[j].Kind]
	})
	for _, a := range apps {
		s := a.status(d, st)
		mark := map[signedIn]string{yes: stepStyle.Render("✓"), no: warnStyle.Render("✗"), unknown: dim("?")}[s]
		fmt.Printf("  %s %-30s %s\n", mark, a.Name, dim(a.Kind))
	}
	fmt.Println(dim("\n  ? = can't be detected; marked done after you confirm.  dotfiles login [--add|--skip|--reset ID]"))
}

// loginPendingCount is shown by `dotfiles status`.
func loginPendingCount(d *Data) int {
	st := readLoginState()
	n := 0
	for _, a := range relevantApps(d, st) {
		if a.status(d, st) == no {
			n++
		}
	}
	return n
}
