package main

import (
	"io"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// specOperations scans api/openapi.yaml for "METHOD /path" pairs: a path is
// a two-space-indented key under "paths:", a method a four-space key below it.
func specOperations(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	pathRE := regexp.MustCompile(`^  (/[^:]*):\s*$`)
	methodRE := regexp.MustCompile(`^    (get|put|post|delete|patch|head|options):\s*$`)
	var ops []string
	inPaths, current := false, ""
	for line := range strings.Lines(string(b)) {
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case inPaths:
			if m := pathRE.FindStringSubmatch(line); m != nil {
				current = m[1]
			} else if m := methodRE.FindStringSubmatch(line); m != nil && current != "" {
				ops = append(ops, strings.ToUpper(m[1])+" "+current)
			}
		}
	}
	slices.Sort(ops)
	return ops
}

func TestOpenAPISpecMatchesRoutes(t *testing.T) {
	routes := newRouter(nil, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil))).Patterns()
	slices.Sort(routes)
	spec := specOperations(t)
	for _, r := range routes {
		if !slices.Contains(spec, r) {
			t.Errorf("route %q is not documented in api/openapi.yaml", r)
		}
	}
	for _, op := range spec {
		if !slices.Contains(routes, op) {
			t.Errorf("api/openapi.yaml documents %q but no such route is registered", op)
		}
	}
}

func TestOpenAPISpecDeclaresErrorSchemaAndVersion(t *testing.T) {
	b, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"openapi: 3.1.0", "    Error:", "request_id"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("spec missing %q", want)
		}
	}
}
