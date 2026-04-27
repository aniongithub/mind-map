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
if [[ "$(uname -s)" == "Darwin" ]]; then
  PLIST_PATH="${HOME}/Library/LaunchAgents/me.anionline.mind-map.plist"
  if [[ -f "$PLIST_PATH" ]]; then
    echo "==> Stopping existing mind-map service..."
    launchctl unload "$PLIST_PATH" 2>/dev/null || true
  fi
elif command -v systemctl >/dev/null 2>&1; then
  if systemctl --user is-active mind-map.service >/dev/null 2>&1; then
    echo "==> Stopping existing mind-map service..."
    systemctl --user stop mind-map.service 2>/dev/null || true
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
USE_SSE=false
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

  mkdir -p "$SERVICE_WIKI_DIR"
  USE_SSE=true

  if [[ "$(uname -s)" == "Darwin" ]]; then
    # macOS: launchd plist
    PLIST_PATH="${HOME}/Library/LaunchAgents/me.anionline.mind-map.plist"
    mkdir -p "$(dirname "$PLIST_PATH")"
    cat > "$PLIST_PATH" << PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>me.anionline.mind-map</string>
    <key>ProgramArguments</key>
    <array>
        <string>${INSTALL_DIR}/mind-map</string>
        <string>serve</string>
        <string>--addr</string>
        <string>:${SERVICE_PORT}</string>
        <string>--dir</string>
        <string>${SERVICE_WIKI_DIR}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${HOME}/.mind-map/mind-map.log</string>
    <key>StandardErrorPath</key>
    <string>${HOME}/.mind-map/mind-map.log</string>
</dict>
</plist>
PLISTEOF
    mkdir -p "${HOME}/.mind-map"
    launchctl unload "$PLIST_PATH" 2>/dev/null || true
    launchctl load "$PLIST_PATH"
    echo "==> Installed and started launchd service"
    echo "    Plist:  ${PLIST_PATH}"
    echo "    Log:    ${HOME}/.mind-map/mind-map.log"
    echo "    Stop:   launchctl unload ${PLIST_PATH}"
    echo "    Start:  launchctl load ${PLIST_PATH}"

  elif command -v systemctl >/dev/null 2>&1; then
    # Linux: systemd user service
    SERVICE_DIR="${HOME}/.config/systemd/user"
    SERVICE_PATH="${SERVICE_DIR}/mind-map.service"
    mkdir -p "$SERVICE_DIR"
    cat > "$SERVICE_PATH" << SVCEOF
[Unit]
Description=mind-map wiki server
After=network.target

[Service]
ExecStart=${INSTALL_DIR}/mind-map serve --addr :${SERVICE_PORT} --dir ${SERVICE_WIKI_DIR}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
SVCEOF
    systemctl --user daemon-reload
    systemctl --user enable mind-map.service
    systemctl --user start mind-map.service
    echo "==> Installed and started systemd user service"
    echo "    Unit:   ${SERVICE_PATH}"
    echo "    Status: systemctl --user status mind-map"
    echo "    Stop:   systemctl --user stop mind-map"
    echo "    Log:    journalctl --user -u mind-map"

  else
    echo "Warning: Could not detect systemd or launchd. Service not installed."
    echo "You can run the server manually:"
    echo "  mind-map serve --addr :${SERVICE_PORT} --dir ${SERVICE_WIKI_DIR}"
  fi

  echo ""
  echo "  Web UI:       http://localhost:${SERVICE_PORT}"
  echo "  MCP endpoint: http://localhost:${SERVICE_PORT}/mcp"
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
  if [ "$USE_SSE" = true ]; then
    mcp_entry="{\"type\": \"sse\", \"url\": \"http://localhost:${SERVICE_PORT}/mcp\"}"
  else
    mcp_entry="{\"command\": \"${INSTALL_DIR}/mind-map\", \"args\": [\"serve\", \"--stdio\"]}"
  fi

  if [ ! -f "$config_file" ]; then
    mkdir -p "$(dirname "$config_file")"
    cat > "$config_file" << MCPEOF
{
  "mcpServers": {
    "mind-map": ${mcp_entry}
  }
}
MCPEOF
    echo "  ✓ ${client_name} — created ${config_file}"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c "
import json
path = '${config_file}'
with open(path) as f:
    data = json.load(f)
servers = data.setdefault('mcpServers', {})
entry = json.loads('${mcp_entry}')
if 'mind-map' not in servers:
    servers['mind-map'] = entry
    with open(path, 'w') as f:
        json.dump(data, f, indent=2)
    print('  ✓ ${client_name} — added to ${config_file}')
else:
    servers['mind-map'] = entry
    with open(path, 'w') as f:
        json.dump(data, f, indent=2)
    print('  ✓ ${client_name} — updated to $([ "$USE_SSE" = true ] && echo "SSE" || echo "stdio") in ${config_file}')
" 2>/dev/null || echo "  ⚠ ${client_name} — could not update ${config_file}"
  else
    echo "  ⚠ ${client_name} — exists but python3 not available to merge"
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
if [ "$USE_SSE" = true ]; then
  echo "Done! mind-map is running as a service."
else
  echo "Done! mind-map is ready."
  echo ""
  echo "  Start the wiki server:  mind-map serve --dir ~/.mind-map/wiki"
  echo "  Start as MCP server:    mind-map serve --stdio --dir ~/.mind-map/wiki"
fi
echo ""
