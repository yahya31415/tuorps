# shellcheck shell=bash
# Network helpers for the stack smoke test. Bash 3.2 compatible (macOS
# /bin/bash) and free of GNU-only tools: macOS has no timeout(1).

# tcp_open HOST PORT: true if a TCP connection to HOST:PORT opens within 5s.
# The connect runs in the background and is polled, then killed at the
# deadline (bash 3.2 has no `wait -n`).
tcp_open() {
	bash -c "exec 3<>/dev/tcp/$1/$2" &
	local pid=$! _
	for _ in $(seq 1 50); do
		if ! kill -0 "$pid" 2>/dev/null; then
			wait "$pid"
			return
		fi
		sleep 0.1
	done
	kill "$pid" 2>/dev/null
	wait "$pid" 2>/dev/null
	return 1
}
