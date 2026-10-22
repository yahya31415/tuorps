# shellcheck shell=bash
# Network helpers for the stack smoke test.

# tcp_open HOST PORT: true if a TCP connection to HOST:PORT opens within 5s.
tcp_open() { timeout 5 bash -c "exec 3<>/dev/tcp/$1/$2"; }
