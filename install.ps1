#Requires -Version 5.1
<#
.SYNOPSIS
    Windows installer for mind-map.
.DESCRIPTION
    Downloads the native Windows binary, configures MCP clients,
    and optionally installs mind-map as a persistent service.
.PARAMETER Version
    Install a specific release tag (e.g. v0.49). Defaults to the latest
    release. Useful for testing prereleases without making them latest.
    Equivalent env var: MINDMAP_VERSION
.EXAMPLE
    irm https://github.com/aniongithub/mind-map/releases/latest/download/install.ps1 | iex
.EXAMPLE
    # Pin a specific release. The piped form can't pass parameters directly,
    # so set the env var first:
    $env:MINDMAP_VERSION = "v0.49"
    irm https://github.com/aniongithub/mind-map/releases/latest/download/install.ps1 | iex
#>
param(
    [string]$Version = ""
)

# Resolve version from -Version or MINDMAP_VERSION env var. The env var is
# how the piped `irm | iex` form supplies a value, since parameters don't
# flow through a pipeline of strings. Explicit -Version wins if both set.
if (-not $Version -and $env:MINDMAP_VERSION) {
    $Version = $env:MINDMAP_VERSION
}

# Auto-elevate to admin (needed for Windows Service installation). When we
# relaunch, propagate the version pin via the env var so the elevated
# process picks it up (param binding doesn't survive `irm | iex`).
if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "Requesting administrative privileges..." -ForegroundColor Yellow
    $scriptUrl = "https://github.com/aniongithub/mind-map/releases/latest/download/install.ps1"
    $envPrefix = ""
    if ($Version) {
        # Set the env var inside the elevated session before running iex.
        $envPrefix = "`$env:MINDMAP_VERSION = '$Version'; "
    }
    Start-Process powershell.exe "-NoExit -NoProfile -ExecutionPolicy Bypass -Command `"& { ${envPrefix}irm '$scriptUrl' | iex }`"" -Verb RunAs
    exit
}

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$Repo = "aniongithub/mind-map"
$InstallDir = "$env:LOCALAPPDATA\mind-map"
$BinaryPath = "$InstallDir\mind-map.exe"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

function Write-Step { param([string]$Message) Write-Host "==> $Message" -ForegroundColor Cyan }
function Write-Ok   { param([string]$Message) Write-Host "  $([char]0x2713) $Message" -ForegroundColor Green }
function Write-Warn { param([string]$Message) Write-Host "  $([char]0x26A0) $Message" -ForegroundColor Yellow }

# ---------------------------------------------------------------------------
# 1. Detect architecture
# ---------------------------------------------------------------------------

Write-Step "Detecting platform..."

$arch = if ([Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "x64" }
} else {
    Write-Host "Error: 32-bit Windows is not supported." -ForegroundColor Red
    exit 1
}

Write-Ok "windows-$arch"

# ---------------------------------------------------------------------------
# 2. Resolve version
# ---------------------------------------------------------------------------

if ($Version) {
    Write-Step "Using pinned version: $Version"
    $version = $Version
} else {
    Write-Step "Checking latest version..."
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
    $version = $release.tag_name
    Write-Ok "Latest version: $version"
}

# ---------------------------------------------------------------------------
# 3. Stop existing service before replacing binary
# ---------------------------------------------------------------------------

# Stop existing service before replacing binary (ignore errors if not installed)
if (Test-Path $BinaryPath) {
    $ErrorActionPreference = "Continue"
    & $BinaryPath service stop 2>&1 | Out-Null
    if ($LASTEXITCODE -eq 0) {
        Write-Ok "Stopped existing mind-map service"
    }
    $ErrorActionPreference = "Stop"
}

# ---------------------------------------------------------------------------
# 4. Download and install binary
# ---------------------------------------------------------------------------

Write-Step "Downloading mind-map-windows-$arch.exe..."

New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null

$artifact = "mind-map-windows-$arch.exe"
$tarball = "$artifact.tar.gz"
$downloadUrl = "https://github.com/$Repo/releases/download/$version/$tarball"
$tarballPath = "$env:TEMP\$tarball"

Invoke-WebRequest -Uri $downloadUrl -OutFile $tarballPath -UseBasicParsing

# Extract using tar (available on Windows 10+)
tar -xzf $tarballPath -C $InstallDir 2>&1 | Out-Null

# Rename platform-specific binary
if (Test-Path "$InstallDir\$artifact") {
    Move-Item -Path "$InstallDir\$artifact" -Destination $BinaryPath -Force
}

Remove-Item $tarballPath -Force -ErrorAction SilentlyContinue

Write-Ok "Installed to $BinaryPath"

# Verify
try {
    & $BinaryPath --help | Out-Null
    Write-Ok "mind-map is working"
} catch {
    Write-Warn "Binary installed but could not verify"
}

# Add to user PATH if not already there
$userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
if ($userPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("PATH", "$InstallDir;$userPath", "User")
    $env:PATH = "$InstallDir;$env:PATH"
    Write-Ok "Added $InstallDir to user PATH"
}

# ---------------------------------------------------------------------------
# 5. Install SKILL.md for agent discovery
# ---------------------------------------------------------------------------

Write-Step "Installing SKILL.md for agent discovery..."

$SkillUrl = "https://raw.githubusercontent.com/$Repo/main/SKILL.md"
$SkillDirs = @(
    "$env:USERPROFILE\.copilot\skills\mind-map"
    "$env:USERPROFILE\.claude\skills\mind-map"
    "$env:USERPROFILE\.agents\skills\mind-map"
    "$env:APPDATA\opencode\skills\mind-map"
)

foreach ($dir in $SkillDirs) {
    try {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
        Invoke-RestMethod -Uri $SkillUrl -OutFile "$dir\SKILL.md"
        Write-Ok "$dir\SKILL.md"
    } catch {
        Write-Warn "Could not install to $dir"
    }
}

# ---------------------------------------------------------------------------
# 6. Interactive: set up as a persistent service
# ---------------------------------------------------------------------------

$DefaultPort = "4242"
$DefaultWikiDir = "$env:ProgramData\mind-map\wiki"
$servicePort = $DefaultPort

function Test-PortAvailable {
    param([int]$Port)
    try {
        $listener = [System.Net.Sockets.TcpClient]::new()
        $listener.Connect("127.0.0.1", $Port)
        $listener.Close()
        return $false  # connection succeeded — port is in use
    } catch {
        return $true   # connection refused — port is free
    }
}

Write-Host ""
$installService = Read-Host "Would you like to install mind-map as a persistent service? [y/N]"

if ($installService -match '^[Yy]$') {
    # --- Port ---
    if (-not (Test-PortAvailable -Port ([int]$DefaultPort))) {
        Write-Warn "Port $DefaultPort is already in use."
    }
    while ($true) {
        if (Test-PortAvailable -Port ([int]$DefaultPort)) {
            $servicePort = Read-Host "Port [$DefaultPort] (enter nothing to auto-pick a free port)"
        } else {
            $servicePort = Read-Host "Enter a port (or nothing to auto-pick a free port)"
        }
        if ([string]::IsNullOrWhiteSpace($servicePort) -and -not (Test-PortAvailable -Port ([int]$DefaultPort))) {
            # Auto-pick: scan from 8080 upward
            $found = $false
            for ($p = 8080; $p -le 8180; $p++) {
                if (Test-PortAvailable -Port $p) {
                    $servicePort = "$p"
                    Write-Ok "Auto-selected port $servicePort"
                    $found = $true
                    break
                }
            }
            if (-not $found) {
                Write-Warn "Could not find a free port. Please enter one manually."
                continue
            }
        }
        if ([string]::IsNullOrWhiteSpace($servicePort)) { $servicePort = $DefaultPort }
        if ($servicePort -notmatch '^\d+$') {
            Write-Warn "Invalid port number."
            continue
        }
        if (-not (Test-PortAvailable -Port ([int]$servicePort))) {
            Write-Warn "Port $servicePort is already in use."
            continue
        }
        break
    }

    $serviceWikiDir = Read-Host "Wiki directory [$DefaultWikiDir]"
    if ([string]::IsNullOrWhiteSpace($serviceWikiDir)) { $serviceWikiDir = $DefaultWikiDir }

    # Build service flags
    $svcFlags = @("--addr", "127.0.0.1:$servicePort", "--dir", "$serviceWikiDir")

    # Uninstall existing service if present (handles reinstall)
    $ErrorActionPreference = "Continue"
    & $BinaryPath service stop 2>&1 | Out-Null
    & $BinaryPath service uninstall 2>&1 | Out-Null
    $ErrorActionPreference = "Stop"

    # Install and start the service (already running as admin)
    & $BinaryPath service install @svcFlags
    & $BinaryPath service start @svcFlags

    Write-Host ""
    $webUrl = "http://localhost:$servicePort"
    Write-Host "  Web UI: $webUrl" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "  Manage with:  mind-map service status|stop|start|uninstall" -ForegroundColor DarkGray
}

# ---------------------------------------------------------------------------
# 7. Configure MCP clients
# ---------------------------------------------------------------------------

Write-Step "Configuring MCP clients..."

$mcpServerEntry = @{
    type    = "local"
    command = $BinaryPath
    args    = @()
    tools   = @("*")
}

function Set-McpConfig {
    param(
        [string]$ConfigPath,
        [string]$ClientName
    )

    try {
        if (Test-Path $ConfigPath) {
            $content = Get-Content -Raw $ConfigPath | ConvertFrom-Json
            if (-not $content.mcpServers) {
                $content | Add-Member -NotePropertyName "mcpServers" -NotePropertyValue ([PSCustomObject]@{})
            }
            if ($content.mcpServers.PSObject.Properties.Name -contains "mind-map") {
                $content.mcpServers.PSObject.Properties.Remove("mind-map")
            }
            $content.mcpServers | Add-Member -NotePropertyName "mind-map" -NotePropertyValue ([PSCustomObject]$mcpServerEntry)
            $content | ConvertTo-Json -Depth 10 | Set-Content $ConfigPath -Encoding UTF8
            Write-Ok "$ClientName — configured in $ConfigPath"
        } else {
            $dir = Split-Path $ConfigPath -Parent
            if ($dir) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
            $config = [PSCustomObject]@{
                mcpServers = [PSCustomObject]@{
                    "mind-map" = [PSCustomObject]$mcpServerEntry
                }
            }
            $config | ConvertTo-Json -Depth 10 | Set-Content $ConfigPath -Encoding UTF8
            Write-Ok "$ClientName — created $ConfigPath"
        }
    } catch {
        Write-Warn "$ClientName — could not update $ConfigPath"
    }
}

# Claude Code
Set-McpConfig "$env:USERPROFILE\.claude.json" "Claude Code"

# GitHub Copilot (if .copilot dir exists)
if (Test-Path "$env:USERPROFILE\.copilot") {
    Set-McpConfig "$env:USERPROFILE\.copilot\mcp-config.json" "GitHub Copilot"
}

# VS Code (if config dir exists)
$vscodeDir = "$env:APPDATA\Code\User"
if (Test-Path $vscodeDir) {
    Set-McpConfig "$vscodeDir\mcp.json" "VS Code"
}

# Cursor (if installed)
if (Test-Path "$env:USERPROFILE\.cursor") {
    Set-McpConfig "$env:USERPROFILE\.cursor\mcp.json" "Cursor"
}

# OpenCode (https://opencode.ai) — different config shape: top-level "mcp"
# (not "mcpServers"), command is an array, and entries carry "enabled": true.
# Primary path on Windows is %APPDATA%\opencode\opencode.json; the script also
# accepts the .jsonc variant if it's the file the user already has.
$openCodeEntry = [PSCustomObject]@{
    type    = "local"
    command = @($BinaryPath)
    enabled = $true
}

function Set-OpenCodeMcpConfig {
    param([string]$ConfigPath)
    try {
        if (Test-Path $ConfigPath) {
            $content = Get-Content -Raw $ConfigPath | ConvertFrom-Json
            if (-not $content.mcp) {
                $content | Add-Member -NotePropertyName "mcp" -NotePropertyValue ([PSCustomObject]@{})
            }
            if ($content.mcp.PSObject.Properties.Name -contains "mind-map") {
                $content.mcp.PSObject.Properties.Remove("mind-map")
            }
            $content.mcp | Add-Member -NotePropertyName "mind-map" -NotePropertyValue $openCodeEntry
            $content | ConvertTo-Json -Depth 10 | Set-Content $ConfigPath -Encoding UTF8
            Write-Ok "OpenCode — configured in $ConfigPath"
        } else {
            $dir = Split-Path $ConfigPath -Parent
            if ($dir) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
            $config = [PSCustomObject]@{
                '$schema' = "https://opencode.ai/config.json"
                mcp       = [PSCustomObject]@{
                    "mind-map" = $openCodeEntry
                }
            }
            $config | ConvertTo-Json -Depth 10 | Set-Content $ConfigPath -Encoding UTF8
            Write-Ok "OpenCode — created $ConfigPath"
        }
    } catch {
        Write-Warn "OpenCode — could not update $ConfigPath"
    }
}

$openCodeDir = "$env:APPDATA\opencode"
$openCodeJson = Join-Path $openCodeDir "opencode.json"
$openCodeJsonc = Join-Path $openCodeDir "opencode.jsonc"
$openCodeCfg = $null
if (Test-Path $openCodeJson) {
    $openCodeCfg = $openCodeJson
} elseif (Test-Path $openCodeJsonc) {
    $openCodeCfg = $openCodeJsonc
} elseif ((Test-Path $openCodeDir) -or (Get-Command opencode -ErrorAction SilentlyContinue)) {
    $openCodeCfg = $openCodeJson
}
if ($openCodeCfg) {
    Set-OpenCodeMcpConfig $openCodeCfg
}

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------

Write-Host ""
if ($installService -match '^[Yy]$') {
    Write-Host "Done! mind-map is running as a service." -ForegroundColor Green
} else {
    Write-Host "Done! mind-map is ready to use." -ForegroundColor Green
    Write-Host ""
    Write-Host "  Start the web UI:  mind-map serve" -ForegroundColor DarkGray
}
Write-Host ""
Write-Host "To uninstall mind-map completely:" -ForegroundColor DarkGray
Write-Host "  mind-map service uninstall                        # remove service (if installed)" -ForegroundColor DarkGray
Write-Host "  Remove-Item -Recurse '$InstallDir'                # remove binary" -ForegroundColor DarkGray
Write-Host "  Remove-Item -Recurse '$env:USERPROFILE\.mind-map' # remove wiki data" -ForegroundColor DarkGray
Write-Host "  Remove-Item -Recurse '$env:USERPROFILE\.copilot\skills\mind-map', '$env:USERPROFILE\.claude\skills\mind-map', '$env:USERPROFILE\.agents\skills\mind-map', '$env:APPDATA\opencode\skills\mind-map'" -ForegroundColor DarkGray
