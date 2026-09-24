package sync

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestScrubToken(t *testing.T) {
	tok := "ghp_supersecret"
	in := "fatal: could not read Password for 'https://x-access-token@github.com': ghp_supersecret"
	got := scrubToken(in, tok)
	if strings.Contains(got, tok) {
		t.Fatalf("token leaked after scrub: %q", got)
	}
	if !strings.Contains(got, "***") {
		t.Fatalf("expected redaction marker, got %q", got)
	}
	// Empty token is a no-op.
	if scrubToken(in, "") != in {
		t.Fatalf("empty token should be a no-op")
	}
}

// TestNewAskpassHelper verifies the generated helper answers git's
// username/password prompts from the environment and that the token is
// never written into the script file on disk.
func TestNewAskpassHelper(t *testing.T) {
	tok := "ghp_tokenvalue123"
	cleanup, env, err := newAskpassHelper(tok)
	if err != nil {
		t.Fatalf("newAskpassHelper: %v", err)
	}
	defer cleanup()

	var script, tokenEnv string
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_ASKPASS=") {
			script = strings.TrimPrefix(e, "GIT_ASKPASS=")
		}
		if strings.HasPrefix(e, "MIND_MAP_SYNC_TOKEN=") {
			tokenEnv = strings.TrimPrefix(e, "MIND_MAP_SYNC_TOKEN=")
		}
	}
	if script == "" {
		t.Fatal("GIT_ASKPASS not set in env")
	}
	if tokenEnv != tok {
		t.Fatalf("token env = %q, want %q", tokenEnv, tok)
	}

	// The script file must not contain the token itself.
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	if strings.Contains(string(body), tok) {
		t.Fatal("token was written into the askpass script file")
	}

	// Username prompt → x-access-token; anything else → the token (from env).
	runPrompt := func(prompt string) string {
		cmd := exec.Command(script, prompt)
		cmd.Env = append(os.Environ(), "MIND_MAP_SYNC_TOKEN="+tok)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("run askpass %q: %v", prompt, err)
		}
		return string(out)
	}
	if got := runPrompt("Username for 'https://github.com': "); got != "x-access-token" {
		t.Fatalf("username answer = %q, want x-access-token", got)
	}
	if got := runPrompt("Password for 'https://x-access-token@github.com': "); got != tok {
		t.Fatalf("password answer = %q, want the token", got)
	}

	// After cleanup the script is gone.
	cleanup()
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Fatalf("askpass script not cleaned up: %v", err)
	}
}
