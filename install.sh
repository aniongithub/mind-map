#!/usr/bin/env bash
set -euo pipefail

# mind-map installer
# Downloads the latest release binary.
#
# Usage:
#   curl -fsSL https://github.com/aniongithub/mind-map/releases/latest/download/install.sh | bash
#   curl -fsSL ... | bash -s -- --install-dir /usr/local/bin

REPO="aniongithub/mind-map"
INSTALL_DIR="${HOME}/.local/bin"
SKIP_MCP_CONFIG=false

# Parse args
while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-dir)       INSTALL_DIR="$2"; shift 2 ;;
    --skip-mcp-config)   SKIP_MCP_CONFIG=true; shift ;;
    --help|-h)
      echo "Usage: install.sh [--install-dir DIR] [--skip-mcp-config]"
      echo "  --install-dir       Installation directory (default: ~/.local/bin)"
      echo "  --skip-mcp-config   Skip MCP client configuration (used by install.ps1)"
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

VERSION="$(get_latest_version)"
if [[ -z "$VERSION" ]]; then
  echo "Error: Could not determine latest release version."
  echo "Check: https://github.com/${REPO}/releases"
  exit 1
fi
echo "==> Latest version: ${VERSION}"

TARBALL_NAME="mind-map-${PLATFORM}.tar.gz"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL_NAME}"

# Create install directory
mkdir -p "$INSTALL_DIR"

# Stop existing service before replacing the binary
if [ -f "${INSTALL_DIR}/mind-map" ]; then
  sudo "${INSTALL_DIR}/mind-map" service stop 2>/dev/null && \
    echo "==> Stopped existing mind-map service" || true
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

# Install SKILL.md for agent discovery
SKILL_URL="https://raw.githubusercontent.com/${REPO}/main/SKILL.md"
SKILL_DIRS=(
  "${HOME}/.copilot/skills/mind-map"
  "${HOME}/.claude/skills/mind-map"
  "${HOME}/.agents/skills/mind-map"
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

DEFAULT_PORT="51849"
DEFAULT_WIKI_DIR="${HOME}/.mind-map/wiki"
SERVICE_PORT="$DEFAULT_PORT"

echo ""
printf "Would you like to install mind-map as a persistent service? [y/N] "
read -r INSTALL_SERVICE < /dev/tty || INSTALL_SERVICE="n"

if [[ "$INSTALL_SERVICE" =~ ^[Yy]$ ]]; then
  printf "Port [${DEFAULT_PORT}]: "
  read -r SERVICE_PORT < /dev/tty || SERVICE_PORT=""
  SERVICE_PORT="${SERVICE_PORT:-$DEFAULT_PORT}"

  printf "Wiki directory [${DEFAULT_WIKI_DIR}]: "
  read -r SERVICE_WIKI_DIR < /dev/tty || SERVICE_WIKI_DIR=""
  SERVICE_WIKI_DIR="${SERVICE_WIKI_DIR:-$DEFAULT_WIKI_DIR}"

  # Use the built-in service manager (kardianos/service)
  # System services require elevated privileges on Linux
  if [[ "$(uname -s)" == "Linux" ]]; then
    echo "==> Installing service (requires sudo)..."
    # Uninstall existing service if present (handles reinstall)
    sudo "${INSTALL_DIR}/mind-map" service stop 2>/dev/null || true
    sudo "${INSTALL_DIR}/mind-map" service uninstall 2>/dev/null || true
    sudo "${INSTALL_DIR}/mind-map" service install --addr ":${SERVICE_PORT}" --dir "${SERVICE_WIKI_DIR}" && \
      sudo "${INSTALL_DIR}/mind-map" service start --addr ":${SERVICE_PORT}" --dir "${SERVICE_WIKI_DIR}"
  else
    "${INSTALL_DIR}/mind-map" service stop 2>/dev/null || true
    "${INSTALL_DIR}/mind-map" service uninstall 2>/dev/null || true
    "${INSTALL_DIR}/mind-map" service install --addr ":${SERVICE_PORT}" --dir "${SERVICE_WIKI_DIR}" && \
      "${INSTALL_DIR}/mind-map" service start --addr ":${SERVICE_PORT}" --dir "${SERVICE_WIKI_DIR}"
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
    python3 -c "
import json
path = '${config_file}'
with open(path) as f:
    data = json.load(f)
servers = data.setdefault('mcpServers', {})
entry = json.loads('${mcp_entry}')
servers['mind-map'] = entry
with open(path, 'w') as f:
    json.dump(data, f, indent=2)
print('  + ${client_name} -- updated ${config_file}')
" 2>/dev/null || echo "  ! ${client_name} -- could not update ${config_file}"
  else
    echo "  ! ${client_name} -- exists but python3 not available to merge"
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
echo "  rm -rf ~/.copilot/skills/mind-map ~/.claude/skills/mind-map ~/.agents/skills/mind-map"
echo ""
