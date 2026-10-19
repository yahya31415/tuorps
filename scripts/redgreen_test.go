package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	modFile     = "module example.com/m\n\ngo 1.27\n"
	addBuggy    = "package a\n\nfunc Add(x, y int) int { return x - y }\n"
	addFixed    = "package a\n\nfunc Add(x, y int) int { return x + y }\n"
	addTest     = "package a\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(1, 2); got != 3 {\n\t\tt.Fatalf(\"Add(1, 2) = %d, want 3\", got)\n\t}\n}\n"
	subTestUndf = "package a\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {\n\tif Sub(3, 1) != 2 {\n\t\tt.Fatal(\"Sub\")\n\t}\n}\n"
)

// goEnv keeps the throwaway module hermetic: no toolchain download, no workspace.
func goEnv() []string { return []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=mod"} }

// newModule builds a tiny module whose package a has a failing TestAdd.
func newModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", modFile)
	writeFile(t, dir, "pkg/a/a.go", addBuggy)
	writeFile(t, dir, "pkg/a/a_test.go", addTest)
	return dir
}

func tdd(t *testing.T, dir string, args ...string) result {
	t.Helper()
	return run(t, dir, goEnv(), "", scriptPath(t, "tdd.sh"), args...)
}

func readRed(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".tdd/red"))
	if err != nil {
		t.Fatalf("read .tdd/red: %v", err)
	}
	return string(b)
}

func TestRedRecordsAFailingTest(t *testing.T) {
	t.Parallel()
	dir := newModule(t)
	r := tdd(t, dir, "red", "./pkg/a", "^TestAdd$")
	if r.code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", r.code, r.output())
	}
	red := readRed(t, dir)
	for _, want := range []string{
		"recorded-by: make red\n",
		"pkg: ./pkg/a\n",
		"run: ^TestAdd$\n",
		"kind: test\n",
		"recorded-at: ",
		"Add(1, 2) = -1, want 3",
	} {
		if !strings.Contains(red, want) {
			t.Errorf(".tdd/red missing %q:\n%s", want, red)
		}
	}
	if !strings.Contains(r.output(), "Add(1, 2) = -1, want 3") {
		t.Errorf("failing output not echoed to the user:\n%s", r.output())
	}
}

func TestRedRefusesWithoutAFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		prepare func(t *testing.T, dir string)
		pkg     string
		run     string
		want    string
	}{
		{"test passes", func(t *testing.T, dir string) { writeFile(t, dir, "pkg/a/a.go", addFixed) }, "./pkg/a", "^TestAdd$", "passed"},
		{"empty run pattern", func(*testing.T, string) {}, "./pkg/a", "", "RUN"},
		{"empty package", func(*testing.T, string) {}, "", "^TestAdd$", "PKG"},
		{"run matches no test", func(*testing.T, string) {}, "./pkg/a", "^TestNothing$", "no tests"},
		{"compile error in unrelated package", func(t *testing.T, dir string) {
			writeFile(t, dir, "pkg/b/b.go", "package b\n\nfunc B() int { return \"x\" }\n")
			writeFile(t, dir, "pkg/a/a.go", "package a\n\nimport \"example.com/m/pkg/b\"\n\nfunc Add(x, y int) int { return x - y + b.B() }\n")
		}, "./pkg/a", "^TestAdd$", "unrelated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := newModule(t)
			tc.prepare(t, dir)
			r := tdd(t, dir, "red", tc.pkg, tc.run)
			if r.code == 0 {
				t.Fatalf("exit = 0, want non-zero\n%s", r.output())
			}
			if !strings.Contains(r.output(), tc.want) {
				t.Errorf("output missing %q:\n%s", tc.want, r.output())
			}
			if exists(filepath.Join(dir, ".tdd/red")) {
				t.Errorf(".tdd/red was written")
			}
		})
	}
}

func TestRedAcceptsCompileErrorInTargetPackage(t *testing.T) {
	t.Parallel()
	dir := newModule(t)
	writeFile(t, dir, "pkg/a/sub_test.go", subTestUndf)
	r := tdd(t, dir, "red", "./pkg/a", "^TestSub$")
	if r.code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", r.code, r.output())
	}
	if red := readRed(t, dir); !strings.Contains(red, "kind: build\n") || !strings.Contains(red, "undefined: Sub") {
		t.Errorf(".tdd/red should record a build failure:\n%s", red)
	}
}

func TestRedAcceptsBrandNewPackageWithOnlyExternalTests(t *testing.T) {
	t.Parallel()
	dir := newModule(t)
	writeFile(t, dir, "pkg/c/c_test.go", "package c_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/m/pkg/c\"\n)\n\nfunc TestC(t *testing.T) { c.C() }\n")
	r := tdd(t, dir, "red", "./pkg/c", "^TestC$")
	if r.code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", r.code, r.output())
	}
	if red := readRed(t, dir); !strings.Contains(red, "kind: build\n") {
		t.Errorf(".tdd/red should record a build failure:\n%s", red)
	}
}

func TestRedRefusesMissingFilesInAnotherPackage(t *testing.T) {
	t.Parallel()
	dir := newModule(t)
	writeFile(t, dir, "pkg/d/d.go", "package d\n\nimport \"example.com/m/pkg/e\"\n\nfunc D() { e.E() }\n")
	writeFile(t, dir, "pkg/e/e_test.go", "package e_test\n")
	writeFile(t, dir, "pkg/d/d_test.go", "package d\n\nimport \"testing\"\n\nfunc TestD(t *testing.T) { D() }\n")
	r := tdd(t, dir, "red", "./pkg/d", "^TestD$")
	if r.code == 0 || !strings.Contains(r.output(), "unrelated") {
		t.Fatalf("exit = %d, want refusal mentioning unrelated\n%s", r.code, r.output())
	}
}

func TestGreenClearsRedOncePassing(t *testing.T) {
	t.Parallel()
	dir := newModule(t)
	if r := tdd(t, dir, "red", "./pkg/a", "^TestAdd$"); r.code != 0 {
		t.Fatalf("red: %s", r.output())
	}
	writeFile(t, dir, "pkg/a/a.go", addFixed)
	r := tdd(t, dir, "green")
	if r.code != 0 {
		t.Fatalf("green exit = %d, want 0\n%s", r.code, r.output())
	}
	if exists(filepath.Join(dir, ".tdd/red")) {
		t.Fatal(".tdd/red still present after green")
	}
}

func TestGreenKeepsRedWhileFailing(t *testing.T) {
	t.Parallel()
	dir := newModule(t)
	if r := tdd(t, dir, "red", "./pkg/a", "^TestAdd$"); r.code != 0 {
		t.Fatalf("red: %s", r.output())
	}
	r := tdd(t, dir, "green")
	if r.code == 0 {
		t.Fatalf("green exit = 0 with failing test\n%s", r.output())
	}
	if !exists(filepath.Join(dir, ".tdd/red")) {
		t.Fatal(".tdd/red removed although tests fail")
	}
}

func TestGreenRequiresWholePackageToPass(t *testing.T) {
	t.Parallel()
	dir := newModule(t)
	if r := tdd(t, dir, "red", "./pkg/a", "^TestAdd$"); r.code != 0 {
		t.Fatalf("red: %s", r.output())
	}
	writeFile(t, dir, "pkg/a/a.go", addFixed)
	writeFile(t, dir, "pkg/a/other_test.go", "package a\n\nimport \"testing\"\n\nfunc TestOther(t *testing.T) { t.Fatal(\"broken elsewhere\") }\n")
	r := tdd(t, dir, "green")
	if r.code == 0 || !strings.Contains(r.output(), "broken elsewhere") {
		t.Fatalf("green should fail on another failing test in the package\n%s", r.output())
	}
	if !exists(filepath.Join(dir, ".tdd/red")) {
		t.Fatal(".tdd/red removed although package fails")
	}
}

func TestGreenWithoutRed(t *testing.T) {
	t.Parallel()
	dir := newModule(t)
	r := tdd(t, dir, "green")
	if r.code == 0 || !strings.Contains(r.output(), "make red") {
		t.Fatalf("exit = %d, want non-zero pointing at make red\n%s", r.code, r.output())
	}
}

func TestMakeTargetsWireThroughToScript(t *testing.T) {
	t.Parallel()
	dir := newModule(t)
	makefile := scriptPath(t, "../Makefile")
	r := run(t, dir, goEnv(), "", "make", "-s", "-f", makefile, "red", "PKG=./pkg/a", "RUN=^TestAdd$")
	if r.code != 0 {
		t.Fatalf("make red exit = %d\n%s", r.code, r.output())
	}
	if red := readRed(t, dir); !strings.Contains(red, "run: ^TestAdd$\n") {
		t.Fatalf("make mangled the run pattern:\n%s", red)
	}
	writeFile(t, dir, "pkg/a/a.go", addFixed)
	if r := run(t, dir, goEnv(), "", "make", "-s", "-f", makefile, "green"); r.code != 0 {
		t.Fatalf("make green exit = %d\n%s", r.code, r.output())
	}
	if exists(filepath.Join(dir, ".tdd/red")) {
		t.Fatal(".tdd/red still present after make green")
	}
}
