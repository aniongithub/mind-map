#Requires -Version 5.1
<#
.SYNOPSIS
    Windows installer for mind-map.
.DESCRIPTION
    Downloads the native Windows binary, configures MCP clients,
    and optionally installs mind-map as a persistent service.
.EXAMPLE
    irm https://github.com/aniongithub/mind-map/releases/latest/download/install.ps1 | iex
#>

# Auto-elevate to admin (needed for Windows Service installation)
if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "Requesting administrative privileges..." -ForegroundColor Yellow
    $scriptUrl = "https://github.com/aniongithub/mind-map/releases/latest/download/install.ps1"
    Start-Process powershell.exe "-NoExit -NoProfile -ExecutionPolicy Bypass -Command `"& { irm '$scriptUrl' | iex }`"" -Verb RunAs
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
# 2. Get latest version
# ---------------------------------------------------------------------------

Write-Step "Checking latest version..."

$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
$version = $release.tag_name
Write-Ok "Latest version: $version"

# ---------------------------------------------------------------------------
# 3. Stop existing service before replacing binary
# ---------------------------------------------------------------------------

# Stop existing service before replacing binary (ignore errors if not installed)
if (Test-Path $BinaryPath) {
    & $BinaryPath service stop 2>$null
    if ($LASTEXITCODE -eq 0) {
        Write-Ok "Stopped existing mind-map service"
    }
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

$DefaultPort = "51849"
$DefaultWikiDir = "$env:USERPROFILE\.mind-map\wiki"
$UseSSE = $false
$servicePort = $DefaultPort

Write-Host ""
$installService = Read-Host "Would you like to install mind-map as a persistent service? [y/N]"

if ($installService -match '^[Yy]$') {
    $servicePort = Read-Host "Port [$DefaultPort]"
    if ([string]::IsNullOrWhiteSpace($servicePort)) { $servicePort = $DefaultPort }

    $serviceWikiDir = Read-Host "Wiki directory [$DefaultWikiDir]"
    if ([string]::IsNullOrWhiteSpace($serviceWikiDir)) { $serviceWikiDir = $DefaultWikiDir }

    $UseSSE = $true

    # Install and start the service (already running as admin)
    & $BinaryPath service install --addr ":$servicePort" --dir "$serviceWikiDir"
    & $BinaryPath service start --addr ":$servicePort" --dir "$serviceWikiDir"

    Write-Host ""
    Write-Host "  Web UI:       http://localhost:$servicePort" -ForegroundColor Cyan
    Write-Host "  MCP endpoint: http://localhost:$servicePort/mcp" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "  Manage with:  mind-map service status|stop|start|uninstall" -ForegroundColor DarkGray
}

# ---------------------------------------------------------------------------
# 7. Configure MCP clients
# ---------------------------------------------------------------------------

Write-Step "Configuring MCP clients..."

if ($UseSSE) {
    $mcpServerEntry = @{
        type = "sse"
        url  = "http://localhost:$servicePort/mcp"
    }
} else {
    $mcpServerEntry = @{
        command = $BinaryPath
        args    = @("serve", "--stdio")
    }
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

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------

Write-Host ""
if ($UseSSE) {
    Write-Host "Done! mind-map is running as a service." -ForegroundColor Green
} else {
    Write-Host "Done! mind-map is ready to use." -ForegroundColor Green
    Write-Host ""
    Write-Host "  Start the wiki server:  mind-map serve --dir $DefaultWikiDir" -ForegroundColor DarkGray
    Write-Host "  Start as MCP server:    mind-map serve --stdio" -ForegroundColor DarkGray
}
Write-Host ""
Write-Host "To uninstall mind-map completely:" -ForegroundColor DarkGray
Write-Host "  mind-map service uninstall                        # remove service (if installed)" -ForegroundColor DarkGray
Write-Host "  Remove-Item -Recurse '$InstallDir'                # remove binary" -ForegroundColor DarkGray
Write-Host "  Remove-Item -Recurse '$env:USERPROFILE\.mind-map' # remove wiki data" -ForegroundColor DarkGray
Write-Host "  Remove-Item -Recurse '$env:USERPROFILE\.copilot\skills\mind-map', '$env:USERPROFILE\.claude\skills\mind-map', '$env:USERPROFILE\.agents\skills\mind-map'" -ForegroundColor DarkGray
