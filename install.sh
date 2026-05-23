#!/usr/bin/env bash
set -euo pipefail

# mind-map installer
# Downloads a release binary. Defaults to the latest release.
#
# Usage:
#   curl -fsSL https://github.com/aniongithub/mind-map/releases/latest/download/install.sh | bash
#   curl -fsSL ... | bash -s -- --install-dir /usr/local/bin
#   curl -fsSL ... | bash -s -- --version v0.49        # pin a specific release
#   MINDMAP_VERSION=v0.49 curl -fsSL ... | bash         # env var equivalent

REPO="aniongithub/mind-map"
INSTALL_DIR="${HOME}/.local/bin"
SKIP_MCP_CONFIG=false
# Pre-seed VERSION from the environment so users can `MINDMAP_VERSION=... curl | bash`
# without needing `bash -s --` plumbing. The --version flag overrides if both are set.
VERSION="${MINDMAP_VERSION:-}"

# Parse args
while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-dir)       INSTALL_DIR="$2"; shift 2 ;;
    --skip-mcp-config)   SKIP_MCP_CONFIG=true; shift ;;
    --version)           VERSION="$2"; shift 2 ;;
    --help|-h)
      echo "Usage: install.sh [--install-dir DIR] [--skip-mcp-config] [--version TAG]"
      echo "  --install-dir       Installation directory (default: ~/.local/bin)"
      echo "  --skip-mcp-config   Skip MCP client configuration (used by install.ps1)"
      echo "  --version TAG       Install a specific release tag (e.g. v0.49). Defaults"
      echo "                      to the latest release. Useful for testing prereleases."
      echo "                      Equivalent env var: MINDMAP_VERSION"
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# Detect OS and architecture
detect_platform() {
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"

  case "$os" in
    Linux)  os="linux" ;;
    Darwin) os="darwin" ;;
    *)      echo "Error: Unsupported OS: $os"; exit 1 ;;
  esac

  case "$arch" in
    x86_64|amd64)  arch="x64" ;;
    aarch64|arm64) arch="arm64" ;;
    *)             echo "Error: Unsupported architecture: $arch"; exit 1 ;;
  esac

  echo "${os}-${arch}"
}

# Get latest release tag from GitHub
get_latest_version() {
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' \
    | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/'
}

PLATFORM="$(detect_platform)"
echo "==> Detected platform: ${PLATFORM}"

if [[ -n "$VERSION" ]]; then
  echo "==> Using pinned version: ${VERSION}"
else
  VERSION="$(get_latest_version)"
  if [[ -z "$VERSION" ]]; then
    echo "Error: Could not determine latest release version."
    echo "Check: https://github.com/${REPO}/releases"
    exit 1
  fi
  echo "==> Latest version: ${VERSION}"
fi

TARBALL_NAME="mind-map-${PLATFORM}.tar.gz"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL_NAME}"

# Create install directory
mkdir -p "$INSTALL_DIR"

# Ensure ~/.mind-map exists and is owned by the current user.
# This must happen before any sudo commands that might create it as root.
mkdir -p "${HOME}/.mind-map"

# Stop existing service before replacing the binary
if [ -f "${INSTALL_DIR}/mind-map" ]; then
  # On macOS the service is a user LaunchAgent; sudo would look for the plist
  # under /var/root instead of ~/Library/LaunchAgents, so don't use sudo.
  if [[ "$(uname -s)" == "Darwin" ]]; then
    "${INSTALL_DIR}/mind-map" service stop 2>/dev/null && \
      echo "==> Stopped existing mind-map service" || true
  else
    sudo "${INSTALL_DIR}/mind-map" service stop 2>/dev/null && \
      echo "==> Stopped existing mind-map service" || true
  fi
fi

echo "==> Downloading ${TARBALL_NAME}..."
curl -fsSL "$DOWNLOAD_URL" | tar xz -C "${INSTALL_DIR}"

# Rename platform-specific binary to just "mind-map"
if [[ -f "${INSTALL_DIR}/mind-map-${PLATFORM}" ]]; then
  mv "${INSTALL_DIR}/mind-map-${PLATFORM}" "${INSTALL_DIR}/mind-map"
fi

chmod +x "${INSTALL_DIR}/mind-map"

# macOS: ad-hoc codesign to avoid Gatekeeper "Killed: 9"
if [[ "$(uname -s)" == "Darwin" ]]; then
  codesign -s - "${INSTALL_DIR}/mind-map" 2>/dev/null && \
    echo "==> Codesigned binary for macOS" || true
fi

echo "==> Installed mind-map to ${INSTALL_DIR}/mind-map"

# Verify
if "${INSTALL_DIR}/mind-map" --help >/dev/null 2>&1; then
  echo "==> mind-map is working"
else
  echo "Warning: Binary downloaded but failed to run. Check platform compatibility."
fi

# Check PATH
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
  echo ""
  echo "Note: ${INSTALL_DIR} is not in your PATH. Add it with:"
  echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
fi

# Install SKILL.md for agent discovery. Fetch from the same git ref as the
# binary so the documented tool surface matches what got installed. Tags
# are valid refs on raw.githubusercontent.com.
SKILL_URL="https://raw.githubusercontent.com/${REPO}/${VERSION}/SKILL.md"
SKILL_DIRS=(
  "${HOME}/.copilot/skills/mind-map"
  "${HOME}/.claude/skills/mind-map"
  "${HOME}/.agents/skills/mind-map"
  "${HOME}/.config/opencode/skills/mind-map"
)

echo ""
echo "==> Installing SKILL.md for agent discovery..."
for dir in "${SKILL_DIRS[@]}"; do
  mkdir -p "$dir"
  curl -fsSL -o "${dir}/SKILL.md" "$SKILL_URL" 2>/dev/null && \
    echo "    ${dir}/SKILL.md" || true
done

# ---------------------------------------------------------------------------
# Interactive: set up as a persistent service
# ---------------------------------------------------------------------------

DEFAULT_PORT="4242"
DEFAULT_WIKI_DIR="${HOME}/.mind-map/wiki"
SERVICE_PORT="$DEFAULT_PORT"

# Check whether a TCP port is available
port_available() {
  if command -v nc >/dev/null 2>&1; then
    ! nc -z 127.0.0.1 "$1" 2>/dev/null
  elif command -v ss >/dev/null 2>&1; then
    ! ss -tlnH "sport = :$1" 2>/dev/null | grep -q .
  else
    # No checker available — assume available
    return 0
  fi
}

echo ""
printf "Would you like to install mind-map as a persistent service? [y/N] "
read -r INSTALL_SERVICE < /dev/tty || INSTALL_SERVICE="n"

if [[ "$INSTALL_SERVICE" =~ ^[Yy]$ ]]; then
  # --- Port ---
  if ! port_available "$DEFAULT_PORT"; then
    echo "  Warning: Port ${DEFAULT_PORT} is already in use."
  fi
  while true; do
    if port_available "$DEFAULT_PORT"; then
      printf "Port [${DEFAULT_PORT}] (enter nothing to auto-pick a free port): "
    else
      printf "Enter a port (or nothing to auto-pick a free port): "
    fi
    read -r SERVICE_PORT < /dev/tty || SERVICE_PORT=""
    if [[ -z "$SERVICE_PORT" ]] && ! port_available "$DEFAULT_PORT"; then
      # Auto-pick: scan from 8080 upward
      for p in $(seq 8080 8180); do
        if port_available "$p"; then
          SERVICE_PORT="$p"
          echo "  Auto-selected port ${SERVICE_PORT}"
          break
        fi
      done
      if [[ -z "$SERVICE_PORT" ]]; then
        echo "  Could not find a free port. Please enter one manually."
        continue
      fi
    fi
    SERVICE_PORT="${SERVICE_PORT:-$DEFAULT_PORT}"
    if ! [[ "$SERVICE_PORT" =~ ^[0-9]+$ ]]; then
      echo "  Invalid port number."
      continue
    fi
    if ! port_available "$SERVICE_PORT"; then
      echo "  Port ${SERVICE_PORT} is already in use."
      continue
    fi
    break
  done

  printf "Wiki directory [${DEFAULT_WIKI_DIR}]: "
  read -r SERVICE_WIKI_DIR < /dev/tty || SERVICE_WIKI_DIR=""
  SERVICE_WIKI_DIR="${SERVICE_WIKI_DIR:-$DEFAULT_WIKI_DIR}"

  # Build service flags
  SVC_FLAGS=(--addr "127.0.0.1:${SERVICE_PORT}" --dir "${SERVICE_WIKI_DIR}")

  # Use the built-in service manager (kardianos/service)
  # System services require elevated privileges on Linux
  if [[ "$(uname -s)" == "Linux" ]]; then
    echo "==> Installing service (requires sudo)..."
    # Ensure wiki dir exists and is owned by the current user
    mkdir -p "${SERVICE_WIKI_DIR}"
    # Uninstall existing service if present (handles reinstall)
    sudo "${INSTALL_DIR}/mind-map" service stop 2>/dev/null || true
    sudo "${INSTALL_DIR}/mind-map" service uninstall 2>/dev/null || true
    sudo "${INSTALL_DIR}/mind-map" service install "${SVC_FLAGS[@]}" && \
      sudo "${INSTALL_DIR}/mind-map" service start "${SVC_FLAGS[@]}"
    # Fix ownership: the service runs as root but agents run as the user.
    # Both need write access to the wiki dir and SQLite database.
    sudo chown -R "$(id -u):$(id -g)" "${SERVICE_WIKI_DIR}"
    sudo chown -R "$(id -u):$(id -g)" "${HOME}/.mind-map"
  else
    "${INSTALL_DIR}/mind-map" service stop 2>/dev/null || true
    "${INSTALL_DIR}/mind-map" service uninstall 2>/dev/null || true
    "${INSTALL_DIR}/mind-map" service install "${SVC_FLAGS[@]}" && \
      "${INSTALL_DIR}/mind-map" service start "${SVC_FLAGS[@]}"
  fi

  echo ""
  echo "  Web UI: http://localhost:${SERVICE_PORT}"
  echo ""
  echo "  Manage with:  sudo mind-map service status|stop|start|uninstall"
fi

# ---------------------------------------------------------------------------
# Auto-configure MCP clients (skipped when called from install.ps1)
# ---------------------------------------------------------------------------

if [ "$SKIP_MCP_CONFIG" = true ]; then
  echo ""
  echo "==> Skipping MCP client configuration (--skip-mcp-config)"
  echo ""
  echo "Done! mind-map binary is installed."
  exit 0
fi

# Configure MCP clients
configure_mcp_client() {
  local config_file="$1"
  local client_name="$2"

  local mcp_entry
  mcp_entry="{\"type\": \"local\", \"command\": \"${INSTALL_DIR}/mind-map\", \"args\": [], \"tools\": [\"*\"]}"

  if [ ! -f "$config_file" ]; then
    mkdir -p "$(dirname "$config_file")"
    cat > "$config_file" << MCPEOF
{
  "mcpServers": {
    "mind-map": ${mcp_entry}
  }
}
MCPEOF
    echo "  + ${client_name} -- created ${config_file}"
  elif command -v python3 >/dev/null 2>&1; then
    # Pass values via env vars to avoid shell-quoting the JSON literal into
    # Python source code (which breaks on every embedded double-quote).
    MM_CFG="$config_file" MM_ENTRY="$mcp_entry" MM_CLIENT="$client_name" \
    python3 -c '
import json, os
path = os.environ["MM_CFG"]
client = os.environ["MM_CLIENT"]
with open(path) as f:
    data = json.load(f)
servers = data.setdefault("mcpServers", {})
servers["mind-map"] = json.loads(os.environ["MM_ENTRY"])
with open(path, "w") as f:
    json.dump(data, f, indent=2)
print(f"  + {client} -- updated {path}")
' || echo "  ! ${client_name} -- could not update ${config_file}"
  else
    echo "  ! ${client_name} -- exists but python3 not available to merge"
  fi
}

# OpenCode uses a different MCP config shape than Claude/Copilot/VS Code/Cursor:
#   - Top-level key is "mcp" (not "mcpServers")
#   - command is an array (not a string + separate args)
#   - "enabled": true is expected on the entry
#   - JSONC (JSON with comments) is supported but we always emit plain JSON
# See https://opencode.ai/docs/mcp-servers
configure_opencode() {
  local config_file="$1"

  # OpenCode entry, written with command as a JSON array.
  local mcp_entry
  mcp_entry="{\"type\": \"local\", \"command\": [\"${INSTALL_DIR}/mind-map\"], \"enabled\": true}"

  if [ ! -f "$config_file" ]; then
    mkdir -p "$(dirname "$config_file")"
    cat > "$config_file" << OPENCODEEOF
{
  "\$schema": "https://opencode.ai/config.json",
  "mcp": {
    "mind-map": ${mcp_entry}
  }
}
OPENCODEEOF
    echo "  + OpenCode -- created ${config_file}"
  elif command -v python3 >/dev/null 2>&1; then
    MM_CFG="$config_file" MM_ENTRY="$mcp_entry" \
    python3 -c '
import json, os
path = os.environ["MM_CFG"]
with open(path) as f:
    data = json.load(f)
servers = data.setdefault("mcp", {})
servers["mind-map"] = json.loads(os.environ["MM_ENTRY"])
with open(path, "w") as f:
    json.dump(data, f, indent=2)
print(f"  + OpenCode -- updated {path}")
' || echo "  ! OpenCode -- could not update ${config_file}"
  else
    echo "  ! OpenCode -- exists but python3 not available to merge"
  fi
}

echo ""
echo "==> Configuring MCP clients..."

# GitHub Copilot CLI
if [ -d "${HOME}/.copilot" ]; then
  configure_mcp_client "${HOME}/.copilot/mcp-config.json" "GitHub Copilot"
fi

# VS Code
if [[ "$(uname -s)" == "Darwin" ]]; then
  VSCODE_DIR="${HOME}/Library/Application Support/Code/User"
else
  VSCODE_DIR="${HOME}/.config/Code/User"
fi
if [ -d "$VSCODE_DIR" ]; then
  configure_mcp_client "${VSCODE_DIR}/mcp.json" "VS Code"
fi

# Cursor
if [ -d "${HOME}/.cursor" ]; then
  configure_mcp_client "${HOME}/.cursor/mcp.json" "Cursor"
fi

# Claude Code
configure_mcp_client "${HOME}/.claude.json" "Claude Code"

# OpenCode (https://opencode.ai). The studio reads opencode.json first, then
# opencode.jsonc. If neither exists but the binary is on PATH or the config
# directory has been created, we write the canonical .json so future runs
# pick it up.
OPENCODE_CFG=""
if [ -f "${HOME}/.config/opencode/opencode.json" ]; then
  OPENCODE_CFG="${HOME}/.config/opencode/opencode.json"
elif [ -f "${HOME}/.config/opencode/opencode.jsonc" ]; then
  OPENCODE_CFG="${HOME}/.config/opencode/opencode.jsonc"
elif [ -d "${HOME}/.config/opencode" ] || command -v opencode >/dev/null 2>&1; then
  OPENCODE_CFG="${HOME}/.config/opencode/opencode.json"
fi
if [ -n "$OPENCODE_CFG" ]; then
  configure_opencode "$OPENCODE_CFG"
fi

echo ""
if [ "$INSTALL_SERVICE" = "y" ] || [ "$INSTALL_SERVICE" = "Y" ]; then
  echo "Done! mind-map is running as a service."
else
  echo "Done! mind-map is ready."
  echo ""
  echo "  Start the web UI:  mind-map serve"
fi
echo ""
echo "To uninstall mind-map completely:"
echo "  sudo mind-map service uninstall   # remove service (if installed)"
echo "  rm ${INSTALL_DIR}/mind-map        # remove binary"
echo "  rm -rf ~/.mind-map                # remove wiki data"
echo "  rm -rf ~/.copilot/skills/mind-map ~/.claude/skills/mind-map ~/.agents/skills/mind-map ~/.config/opencode/skills/mind-map"
echo ""
