package app

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Minimal WebDriver BiDi client for Zen/Firefox: list tabs and click buttons by their text.
// Used by `dotfiles login` to click "Continue" / "Open <App>" on sign-in pages in your own
// (already signed-in) browser profile.

var bidiPort = envInt("DOTFILES_BIDI_PORT", 9222)

type bidi struct {
	conn *websocket.Conn
	mu   sync.Mutex
	id   int
	wait map[int]chan json.RawMessage
}

func dialBiDi() (*bidi, error) {
	c, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://127.0.0.1:%d/session", bidiPort), nil)
	if err != nil {
		return nil, err
	}
	b := &bidi{conn: c, wait: map[int]chan json.RawMessage{}}
	go b.readLoop()
	if _, err := b.call("session.new", map[string]any{"capabilities": map[string]any{}}); err != nil {
		c.Close()
		return nil, err
	}
	return b, nil
}

func (b *bidi) readLoop() {
	for {
		_, msg, err := b.conn.ReadMessage()
		if err != nil {
			b.mu.Lock()
			for _, ch := range b.wait {
				close(ch)
			}
			b.wait = map[int]chan json.RawMessage{}
			b.mu.Unlock()
			return
		}
		var head struct {
			ID *int `json:"id"`
		}
		if json.Unmarshal(msg, &head) == nil && head.ID != nil {
			b.mu.Lock()
			if ch, ok := b.wait[*head.ID]; ok {
				ch <- msg
				delete(b.wait, *head.ID)
			}
			b.mu.Unlock()
		}
	}
}

func (b *bidi) call(method string, params any) (json.RawMessage, error) {
	b.mu.Lock()
	b.id++
	id := b.id
	ch := make(chan json.RawMessage, 1)
	b.wait[id] = ch
	err := b.conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params})
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("browser connection closed")
		}
		var resp struct {
			Type    string          `json:"type"`
			Result  json.RawMessage `json:"result"`
			Message string          `json:"message"`
		}
		_ = json.Unmarshal(msg, &resp)
		if resp.Type == "error" {
			return nil, fmt.Errorf("%s: %s", method, resp.Message)
		}
		return resp.Result, nil
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("%s: timed out", method)
	}
}

type tab struct {
	Context string `json:"context"`
	URL     string `json:"url"`
}

func (b *bidi) tabs() ([]tab, error) {
	res, err := b.call("browsingContext.getTree", map[string]any{"maxDepth": 0})
	if err != nil {
		return nil, err
	}
	var t struct {
		Contexts []tab `json:"contexts"`
	}
	return t.Contexts, json.Unmarshal(res, &t)
}

// clickJS clicks the first visible button/link whose text matches one of the labels
// (and only if the page mentions `require`). Returns the label clicked, or null.
const clickJS = `(labels, require) => {
  const text = (document.body && document.body.innerText) || "";
  if (require && !text.toLowerCase().includes(require.toLowerCase())) return null;
  const els = [...document.querySelectorAll('button, a, [role="button"], input[type="submit"], input[type="button"]')];
  const visible = e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && !e.disabled; };
  for (const label of labels) {
    const want = label.toLowerCase();
    const el = els.find(e => visible(e) && ((e.innerText || e.value || e.getAttribute('aria-label') || '').trim().toLowerCase() === want));
    if (el) { el.click(); return label; }
  }
  return null;
}`

func (b *bidi) clickByText(ctx string, labels []string, require string) (string, error) {
	args := []map[string]any{}
	for _, l := range labels {
		args = append(args, map[string]any{"type": "string", "value": l})
	}
	res, err := b.call("script.callFunction", map[string]any{
		"functionDeclaration": clickJS,
		"target":              map[string]any{"context": ctx},
		"arguments":           []any{map[string]any{"type": "array", "value": args}, map[string]any{"type": "string", "value": require}},
		"awaitPromise":        false,
		"userActivation":      true,
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Result struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"result"`
	}
	_ = json.Unmarshal(res, &r)
	if r.Result.Type == "string" {
		return r.Result.Value, nil
	}
	return "", nil
}

func (b *bidi) navigate(ctx, url string) error {
	_, err := b.call("browsingContext.navigate", map[string]any{"context": ctx, "url": url, "wait": "none"})
	return err
}

func (b *bidi) newTab(url string) error {
	res, err := b.call("browsingContext.create", map[string]any{"type": "tab"})
	if err != nil {
		return err
	}
	var r struct {
		Context string `json:"context"`
	}
	_ = json.Unmarshal(res, &r)
	return b.navigate(r.Context, url)
}

func (b *bidi) close() { _ = b.conn.Close() }

// ---------------------------------------------------------------------------- Zen with remote control

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err == nil {
		c.Close()
		return true
	}
	return false
}

func quitZen() error {
	if !zenRunning() {
		return nil
	}
	_ = Run("osascript", "-e", `quit app "Zen"`)
	for i := 0; i < 40 && zenRunning(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if zenRunning() {
		return fmt.Errorf("Zen didn't quit; close it and try again")
	}
	return nil
}

// zenSession restarts Zen with remote control for the duration of `dotfiles login`, and lets sign-in
// pages open their apps without the per-site "open this link?" prompt. Restore() undoes both.
type zenSession struct {
	prof      string
	prefLine  string // the original pref line ("" if absent)
	handlers  map[string]any
	schemes   []string
	restarted bool
}

const extProtoPref = "security.external_protocol_requires_permission"

func startZenSession(schemes []string) (*zenSession, *bidi, error) {
	if portOpen(bidiPort) {
		b, err := dialBiDi()
		return &zenSession{}, b, err
	}
	prof := zenProfile()
	if prof == "" {
		return nil, nil, fmt.Errorf("no Zen profile found (open Zen once, or run `dotfiles zen-push` from another machine)")
	}
	if zenRunning() && !confirm("Zen will restart with remote control for sign-ins (your tabs come back). OK?") {
		return nil, nil, fmt.Errorf("cancelled")
	}
	if err := quitZen(); err != nil {
		return nil, nil, err
	}
	s := &zenSession{prof: prof, schemes: schemes, restarted: true}
	s.prefLine = setPrefLine(prof, extProtoPref, "false")
	s.handlers = setHandlersNoAsk(prof, schemes)
	if err := Run("open", "-a", "Zen", "--args", "--remote-debugging-port", fmt.Sprint(bidiPort)); err != nil {
		s.Restore()
		return nil, nil, err
	}
	for i := 0; i < 60 && !portOpen(bidiPort); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	b, err := dialBiDi()
	if err != nil {
		s.Restore()
		return nil, nil, fmt.Errorf("couldn't connect to Zen: %w", err)
	}
	return s, b, nil
}

// Restore quits remote-controlled Zen, puts the pref and handlers back, and reopens Zen normally.
func (s *zenSession) Restore() {
	if !s.restarted {
		return
	}
	_ = quitZen()
	restorePrefLine(s.prof, extProtoPref, s.prefLine)
	restoreHandlers(s.prof, s.handlers)
	_ = Run("open", "-a", "Zen")
	s.restarted = false
}

var prefRe = func(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^user_pref\("` + regexp.QuoteMeta(name) + `",\s*[^)]*\);\n?`)
}

func setPrefLine(prof, name, value string) string {
	path := filepath.Join(prof, "prefs.js")
	b, _ := os.ReadFile(path)
	s := string(b)
	orig := prefRe(name).FindString(s)
	s = prefRe(name).ReplaceAllString(s, "")
	s += fmt.Sprintf("user_pref(%q, %s);\n", name, value)
	_ = os.WriteFile(path, []byte(s), 0o600)
	return orig
}

func restorePrefLine(prof, name, orig string) {
	path := filepath.Join(prof, "prefs.js")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	s := prefRe(name).ReplaceAllString(string(b), "")
	if orig != "" {
		s += strings.TrimRight(orig, "\n") + "\n"
	}
	_ = os.WriteFile(path, []byte(s), 0o600)
}

// setHandlersNoAsk makes Zen open the given URL schemes in their apps without asking.
// Returns the original entries so they can be restored.
func setHandlersNoAsk(prof string, schemes []string) map[string]any {
	path := filepath.Join(prof, "handlers.json")
	var h map[string]any
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &h) != nil {
		return nil
	}
	sch, _ := h["schemes"].(map[string]any)
	if sch == nil {
		sch = map[string]any{}
		h["schemes"] = sch
	}
	orig := map[string]any{}
	for _, s := range schemes {
		orig[s] = sch[s]                                   // nil if absent
		sch[s] = map[string]any{"action": 4, "ask": false} // 4 = use the system default app
	}
	b, _ := json.Marshal(h)
	_ = os.WriteFile(path, b, 0o600)
	return orig
}

func restoreHandlers(prof string, orig map[string]any) {
	if orig == nil {
		return
	}
	path := filepath.Join(prof, "handlers.json")
	var h map[string]any
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &h) != nil {
		return
	}
	sch, _ := h["schemes"].(map[string]any)
	for s, v := range orig {
		if v == nil {
			delete(sch, s)
		} else {
			sch[s] = v
		}
	}
	b, _ := json.Marshal(h)
	_ = os.WriteFile(path, b, 0o600)
}

func envInt(k string, def int) int {
	var n int
	if _, err := fmt.Sscan(os.Getenv(k), &n); err == nil && n > 0 {
		return n
	}
	return def
}
