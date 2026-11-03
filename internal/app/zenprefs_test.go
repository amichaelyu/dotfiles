package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZenPrefAndHandlersRestore(t *testing.T) {
	dir := t.TempDir()
	prefs := "user_pref(\"a.b\", 1);\nuser_pref(\"security.external_protocol_requires_permission\", true);\nuser_pref(\"c.d\", \"x\");\n"
	handlers := `{"defaultHandlersVersion":{},"mimeTypes":{},"schemes":{"slack":{"action":2,"handlers":[{"name":"Slack"}]},"mailto":{"action":4}}}`
	os.WriteFile(filepath.Join(dir, "prefs.js"), []byte(prefs), 0o600)
	os.WriteFile(filepath.Join(dir, "handlers.json"), []byte(handlers), 0o600)

	orig := setPrefLine(dir, extProtoPref, "false")
	h := setHandlersNoAsk(dir, []string{"slack", "figma"})
	b, _ := os.ReadFile(filepath.Join(dir, "prefs.js"))
	if !strings.Contains(string(b), `"security.external_protocol_requires_permission", false`) || strings.Count(string(b), extProtoPref) != 1 {
		t.Fatalf("pref not set once:\n%s", b)
	}
	hb, _ := os.ReadFile(filepath.Join(dir, "handlers.json"))
	if !strings.Contains(string(hb), `"figma":{"action":4,"ask":false}`) {
		t.Fatalf("handlers not set: %s", hb)
	}
	// simulate Zen adding a pref during the session
	f, _ := os.OpenFile(filepath.Join(dir, "prefs.js"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("user_pref(\"new.pref\", 2);\n")
	f.Close()

	restorePrefLine(dir, extProtoPref, orig)
	restoreHandlers(dir, h)
	b, _ = os.ReadFile(filepath.Join(dir, "prefs.js"))
	for _, want := range []string{`"a.b", 1`, `"c.d", "x"`, `"new.pref", 2`, `"security.external_protocol_requires_permission", true`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s after restore:\n%s", want, b)
		}
	}
	hb, _ = os.ReadFile(filepath.Join(dir, "handlers.json"))
	if strings.Contains(string(hb), "figma") || !strings.Contains(string(hb), `"slack":{"action":2`) {
		t.Fatalf("handlers not restored: %s", hb)
	}
}
