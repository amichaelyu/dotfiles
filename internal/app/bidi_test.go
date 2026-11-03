package app

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestBiDiClick drives a throwaway Zen profile (never your real one): opens a page with a
// "Continue" button and checks clickByText finds and clicks it. Run with ZEN_BIDI_TEST=1.
func TestBiDiClick(t *testing.T) {
	if os.Getenv("ZEN_BIDI_TEST") == "" {
		t.Skip("set ZEN_BIDI_TEST=1 to run (launches a temporary Zen profile)")
	}
	bidiPort = 9333
	prof := t.TempDir()
	cmd := exec.Command("/Applications/Zen.app/Contents/MacOS/zen", "--profile", prof, "--no-remote", "--headless",
		"--remote-debugging-port", fmt.Sprint(bidiPort))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	for i := 0; i < 60 && !portOpen(bidiPort); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	b, err := dialBiDi()
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	page := `data:text/html,<p>Sign in to Codex</p><button onclick="document.title='clicked'">Continue</button><a href="#">Cancel</a>`
	if err := b.newTab(page); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	tabs, err := b.tabs()
	if err != nil {
		t.Fatal(err)
	}
	var ctx string
	for _, tb := range tabs {
		if strings.HasPrefix(tb.URL, "data:") {
			ctx = tb.Context
		}
	}
	if ctx == "" {
		t.Fatalf("test tab not found in %v", tabs)
	}
	if got, _ := b.clickByText(ctx, []string{"Open App", "Continue"}, "Something else"); got != "" {
		t.Fatalf("clicked despite missing required text: %q", got)
	}
	got, err := b.clickByText(ctx, []string{"Open App", "Continue"}, "Codex")
	if err != nil || got != "Continue" {
		t.Fatalf("clickByText = %q, %v", got, err)
	}
	res, err := b.call("script.evaluate", map[string]any{"expression": "document.title", "target": map[string]any{"context": ctx}, "awaitPromise": false})
	if err != nil || !strings.Contains(string(res), "clicked") {
		t.Fatalf("button handler didn't run: %s %v", res, err)
	}
}
