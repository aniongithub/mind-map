#!/usr/bin/env bash
# Runs on the HOST (not in the container) before docker run. Picks a
# free TCP port for the mind-map server and writes it to
# .devcontainer/ports.env. Launch configurations, tasks, the screenshot
# harness, and ad-hoc commands read the file when they run. Keeping the
# file as the single source of truth avoids stale container environment
# values when an existing devcontainer is restarted.
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

# Keep this shell-sourceable for tasks and simple to parse from launch.json.
cat > ports.env <<EOF
MIND_MAP_HOST_PORT=$PORT
EOF

echo "devcontainer host port: $PORT (written to .devcontainer/ports.env)"
