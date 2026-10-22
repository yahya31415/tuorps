package scripts_test

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// noTimeout returns a PATH on which timeout(1) is missing, as on macOS:
// the stub fails the way the shell does for an unknown command.
func noTimeout(t *testing.T) string {
	t.Helper()
	shims := t.TempDir()
	writeFile(t, shims, "timeout", "#!/bin/sh\necho 'timeout: command not found' >&2\nexit 127\n")
	if err := os.Chmod(filepath.Join(shims, "timeout"), 0o755); err != nil {
		t.Fatal(err)
	}
	return "PATH=" + shims + string(os.PathListSeparator) + os.Getenv("PATH")
}

// tcpOpen sources lib/net.sh and runs `tcp_open HOST PORT` without
// timeout(1) on PATH, reporting the result and how long it took.
func tcpOpen(t *testing.T, host string, port int) (result, time.Duration) {
	t.Helper()
	sh := os.Getenv("TDD_SCRIPT_SHELL")
	if sh == "" {
		sh = "bash"
	}
	start := time.Now()
	r := run(t, t.TempDir(), []string{noTimeout(t)}, "", sh,
		"-c", `. "$1" && tcp_open "$2" "$3"`, "tcp_open", scriptPath(t, "lib/net.sh"), host, strconv.Itoa(port))
	return r, time.Since(start)
}

func TestTCPOpenWithoutTimeoutCommand(t *testing.T) {
	t.Parallel()

	t.Run("succeeds on a listening port", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		r, took := tcpOpen(t, "127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
		if r.code != 0 {
			t.Fatalf("exit = %d, want 0\n%s", r.code, r.output())
		}
		if took > 3*time.Second {
			t.Errorf("took %v; an open port should answer at once", took)
		}
	})

	t.Run("fails on a closed port", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		if r, _ := tcpOpen(t, "127.0.0.1", port); r.code == 0 {
			t.Fatalf("exit = 0, want failure for closed port %d", port)
		}
	})

	t.Run("gives up on an unanswered address within the deadline", func(t *testing.T) {
		t.Parallel()
		// 192.0.2.0/24 is TEST-NET-1 (RFC 5737): never routed, so a SYN
		// goes unanswered (or fails at once where there is no route).
		r, took := tcpOpen(t, "192.0.2.1", 80)
		if r.code == 0 {
			t.Fatal("exit = 0, want failure for an unreachable address")
		}
		if took > 8*time.Second {
			t.Errorf("took %v, want the 5s deadline to cut the connect short", took)
		}
	})
}
