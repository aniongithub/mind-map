#!/usr/bin/env bash
# Runs on the HOST (not in the container) before docker run. Picks a
# free TCP port for the mind-map server and writes it to
# .devcontainer/ports.env. The value is consumed in two places:
#
#   1. Inside the container, via runArgs --env-file, so the mind-map
#      binary's `serve --addr` reads $MIND_MAP_HOST_PORT.
#   2. On the host, by tools (launch.json, the screenshot harness,
#      ad-hoc curl) that need to know what URL to hit.
#
# Because the container runs with --network host (see devcontainer.json
# runArgs), there is no port forwarding involved — the container binds
# directly to the host's network namespace, so the same port number is
# the host port. That's why we don't need ${localEnv:...} substitution
# in appPort (which doesn't work for values produced by
# initializeCommand anyway, since appPort substitution happens before
# initializeCommand runs).
#
# See: [[preferences/devcontainer-ports]] in the mind-map wiki for the
# full pattern and the rationale.
set -euo pipefail

# Preferred starting points. The value is stable across normal runs
# when the preferred slot is free, so browser history / bookmarks /
# muscle memory keep working. Only drifts when there's a real
# collision (another worktree's devcontainer, a stray host process,
# a VS Code port-forwarding daemon squatting on it).
PREFERRED=(51888 51889 51890 51891 51892 51893)

pick_port() {
  local p
  for p in "${PREFERRED[@]}"; do
    if ! ss -tln "sport = :$p" 2>/dev/null | grep -q LISTEN; then
      echo "$p"
      return
    fi
  done
  # Kernel-assigned fallback. Bind a socket to port 0, read what we
  # got, close it. Tiny TOCTOU window before the container claims it;
  # in practice we don't hit it.
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

PORT=$(pick_port)

# `cd` so the relative path is stable regardless of where the
# devcontainer CLI was invoked from.
cd "$(dirname "$0")"

# `--env-file` (used in runArgs) accepts bare KEY=VALUE lines. Don't
# quote the value — docker chokes on that.
cat > ports.env <<EOF
MIND_MAP_HOST_PORT=$PORT
EOF

echo "devcontainer host port: $PORT (written to .devcontainer/ports.env)"
