package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// noGNURealpath returns a PATH whose realpath behaves like BSD/macOS
// realpath given GNU-only flags: it fails. The scripts must not depend on it.
func noGNURealpath(t *testing.T) string {
	t.Helper()
	shims := t.TempDir()
	writeFile(t, shims, "realpath", "#!/bin/sh\necho 'realpath: illegal option -- m' >&2\nexit 1\n")
	if err := os.Chmod(filepath.Join(shims, "realpath"), 0o755); err != nil {
		t.Fatal(err)
	}
	return "PATH=" + shims + string(os.PathListSeparator) + os.Getenv("PATH")
}

func TestGuardWorksWithoutGNURealpath(t *testing.T) {
	t.Parallel()
	pathEnv := noGNURealpath(t)
	cases := []struct {
		name, path string
		state      redState
		want       int
	}{
		{"test file allowed", "internal/x/core/a_test.go", noRed, 0},
		{"production file blocked", "internal/x/core/a.go", noRed, 2},
		{"production file allowed after red", "internal/x/core/a.go", freshRed, 0},
		{"traversal out of docs blocked", "docs/../internal/x/core/a.go", noRed, 2},
		{"traversal out of the project allowed", "../elsewhere/main.go", noRed, 0},
		{"state dir blocked", "internal/../.tdd/red", noRed, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project := setupProject(t, tc.state)
			r := guardEdit(t, project, "Edit", project+"/"+tc.path, pathEnv)
			if r.code != tc.want {
				t.Fatalf("exit = %d, want %d\nstderr: %s", r.code, tc.want, r.stderr)
			}
		})
	}
}

func TestRedGreenWorkWithoutGNURealpath(t *testing.T) {
	t.Parallel()
	pathEnv := noGNURealpath(t)
	tddNoRealpath := func(t *testing.T, dir string, args ...string) result {
		t.Helper()
		return run(t, dir, append(goEnv(), pathEnv), "", scriptPath(t, "tdd.sh"), args...)
	}

	t.Run("records a failing test and clears it once green", func(t *testing.T) {
		t.Parallel()
		dir := newModule(t)
		if r := tddNoRealpath(t, dir, "red", "./pkg/a", "^TestAdd$"); r.code != 0 {
			t.Fatalf("red exit = %d\n%s", r.code, r.output())
		}
		writeFile(t, dir, "pkg/a/a.go", addFixed)
		if r := tddNoRealpath(t, dir, "green"); r.code != 0 {
			t.Fatalf("green exit = %d\n%s", r.code, r.output())
		}
	})
	t.Run("accepts a compile error in the target package", func(t *testing.T) {
		t.Parallel()
		dir := newModule(t)
		writeFile(t, dir, "pkg/a/sub_test.go", subTestUndf)
		if r := tddNoRealpath(t, dir, "red", "./pkg/a", "^TestSub$"); r.code != 0 {
			t.Fatalf("exit = %d\n%s", r.code, r.output())
		}
	})
	t.Run("refuses a compile error elsewhere", func(t *testing.T) {
		t.Parallel()
		dir := newModule(t)
		writeFile(t, dir, "pkg/b/b.go", "package b\n\nfunc B() int { return \"x\" }\n")
		writeFile(t, dir, "pkg/a/a.go", "package a\n\nimport \"example.com/m/pkg/b\"\n\nfunc Add(x, y int) int { return x - y + b.B() }\n")
		r := tddNoRealpath(t, dir, "red", "./pkg/a", "^TestAdd$")
		if r.code == 0 || !strings.Contains(r.output(), "unrelated") {
			t.Fatalf("exit = %d, want refusal\n%s", r.code, r.output())
		}
	})
}

// Claude Code treats any exit other than 2 as a non-blocking hook error, so
// an internal failure that exits 1 would let the edit through. The guard
// must turn every unexpected failure into a block.
func TestGuardFailsClosedOnInternalErrors(t *testing.T) {
	t.Parallel()
	realJQ, err := exec.LookPath("jq")
	if err != nil {
		t.Fatalf("jq is required for the guard tests: %v", err)
	}
	shims := t.TempDir()
	// Parses the payload fine, then fails on every field lookup.
	flaky := writeFile(t, shims, "jq", "#!/bin/sh\ncase \"$*\" in\n*'type == \"object\"'*) exec "+realJQ+" \"$@\" ;;\nesac\nexit 5\n")
	if err := os.Chmod(flaky, 0o755); err != nil {
		t.Fatal(err)
	}
	project := setupProject(t, noRed)
	for _, path := range []string{"internal/x/core/a.go", "internal/x/core/a_test.go"} {
		r := guardEdit(t, project, "Edit", filepath.Join(project, path), "TDD_GUARD_JQ="+flaky)
		if r.code != 2 || !strings.Contains(r.stderr, "internal error") {
			t.Errorf("%s: exit = %d, stderr = %q; want 2 mentioning internal error", path, r.code, r.stderr)
		}
	}
}
