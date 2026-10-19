// Package scripts_test specifies the behaviour of the repository's shell
// tooling (TDD guard hook, red/green workflow, coverage gate). The scripts
// are exercised as black boxes: each test builds a throwaway project
// directory, runs the script, and asserts on exit code and output.
package scripts_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type result struct {
	code   int
	stdout string
	stderr string
}

func (r result) output() string { return r.stdout + r.stderr }

// scriptPath returns the absolute path of a script that lives next to the tests.
func scriptPath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(name)
	if err != nil {
		t.Fatalf("abs %s: %v", name, err)
	}
	return p
}

// run executes name with args in dir, feeding stdin, with env appended to
// the current environment. When TDD_SCRIPT_SHELL is set (CI sets it to
// macOS's stock /bin/bash 3.2), *.sh scripts run under that shell instead
// of their shebang.
func run(t *testing.T, dir string, env []string, stdin string, name string, args ...string) result {
	t.Helper()
	if sh := os.Getenv("TDD_SCRIPT_SHELL"); sh != "" && strings.HasSuffix(name, ".sh") {
		name, args = sh, append([]string{name}, args...)
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %s %v: %v", name, args, err)
		}
		code = exitErr.ExitCode()
	}
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// writeFile creates dir/rel (and parents) with content.
func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hookJSON renders a Claude Code PreToolUse payload.
func hookJSON(t *testing.T, tool string, input map[string]any, cwd string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       tool,
		"tool_input":      input,
		"cwd":             cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// age sets the modification time of path to now-d.
func age(t *testing.T, path string, d time.Duration) {
	t.Helper()
	ts := time.Now().Add(-d)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}
