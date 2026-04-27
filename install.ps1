#Requires -Version 5.1
<#
.SYNOPSIS
    Windows installer for mind-map (via WSL).
.DESCRIPTION
    Installs the mind-map Linux binary inside WSL and configures
    Windows-side MCP clients to use the WSL bridge ("command": "wsl").
.EXAMPLE
    irm https://github.com/aniongithub/mind-map/releases/latest/download/install.ps1 | iex
#>

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$Repo = "aniongithub/mind-map"
$WslBinaryPath = "~/.local/bin/mind-map"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

function Write-Step { param([string]$Message) Write-Host "==> $Message" -ForegroundColor Cyan }
function Write-Ok   { param([string]$Message) Write-Host "  $([char]0x2713) $Message" -ForegroundColor Green }
function Write-Warn { param([string]$Message) Write-Host "  $([char]0x26A0) $Message" -ForegroundColor Yellow }
function Write-Fail { param([string]$Message) Write-Host "  $([char]0x2717) $Message" -ForegroundColor Red }

# ---------------------------------------------------------------------------
# 1. Verify WSL is available
# ---------------------------------------------------------------------------

Write-Step "Checking for WSL..."

try {
    $wslStatus = wsl --status 2>&1
    if ($LASTEXITCODE -ne 0) { throw "WSL returned non-zero exit code" }
    Write-Ok "WSL is available"
} catch {
    Write-Host ""
    Write-Host "Error: WSL (Windows Subsystem for Linux) is required but not found." -ForegroundColor Red
    Write-Host ""
    Write-Host "Install WSL with:  wsl --install" -ForegroundColor Yellow
    Write-Host "Then restart your computer and run this script again."
    Write-Host "More info: https://learn.microsoft.com/en-us/windows/wsl/install"
    exit 1
}

# Find usable WSL distros (skip docker-desktop* distros which are minimal)
$WslDistro = $null
$distroLines = (wsl -l -q 2>&1) -replace "`0", "" | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" }
$usableDistros = @($distroLines | Where-Object { $_ -notmatch '^docker-desktop' })

if ($usableDistros.Count -eq 0) {
    Write-Host ""
    Write-Host "Error: No usable WSL distro found (docker-desktop is not supported)." -ForegroundColor Red
    Write-Host ""
    Write-Host "Install a Linux distro with:  wsl --install Ubuntu" -ForegroundColor Yellow
    exit 1
} elseif ($usableDistros.Count -eq 1) {
    $WslDistro = $usableDistros[0]
} else {
    Write-Host ""
    Write-Host "Available WSL distros:" -ForegroundColor Cyan
    for ($i = 0; $i -lt $usableDistros.Count; $i++) {
        Write-Host "  [$($i + 1)] $($usableDistros[$i])"
    }
    Write-Host ""
    $choice = Read-Host "Select a distro (1-$($usableDistros.Count))"
    $idx = [int]$choice - 1
    if ($idx -lt 0 -or $idx -ge $usableDistros.Count) {
        Write-Host "Invalid selection." -ForegroundColor Red
        exit 1
    }
    $WslDistro = $usableDistros[$idx]
}

Write-Ok "Using WSL distro: $WslDistro"

# ---------------------------------------------------------------------------
# 2. Install binary inside WSL (reuse install.sh)
# ---------------------------------------------------------------------------

Write-Step "Installing mind-map binary inside WSL..."

$installUrl = "https://github.com/$Repo/releases/latest/download/install.sh"
$wslResult = wsl -d $WslDistro bash -c "curl -fsSL '$installUrl' | bash -s -- --skip-mcp-config" 2>&1
$wslResult | ForEach-Object { Write-Host "    $_" }

if ($LASTEXITCODE -ne 0) {
    Write-Host ""
    Write-Host "Error: Binary installation inside WSL failed." -ForegroundColor Red
    Write-Host "Try running manually in WSL: curl -fsSL $installUrl | bash"
    exit 1
}

# Verify the binary works
$versionCheck = wsl -d $WslDistro bash -lc "$WslBinaryPath --help" 2>&1
if ($LASTEXITCODE -eq 0) {
    Write-Ok "Installed: mind-map"
} else {
    Write-Warn "Binary installed but could not verify"
}

# ---------------------------------------------------------------------------
# 3. Configure Windows-side MCP clients (with WSL bridge)
# ---------------------------------------------------------------------------

Write-Step "Configuring MCP clients..."

$mcpServerEntry = @{
    command = "wsl"
    args    = @($WslBinaryPath, "serve", "--stdio")
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
                Write-Ok "$ClientName — already configured"
                return
            }
            $content.mcpServers | Add-Member -NotePropertyName "mind-map" -NotePropertyValue ([PSCustomObject]$mcpServerEntry)
            $content | ConvertTo-Json -Depth 10 | Set-Content $ConfigPath -Encoding UTF8
            Write-Ok "$ClientName — added to $ConfigPath"
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
# 4. Install SKILL.md for agent discovery
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
# Done
# ---------------------------------------------------------------------------

Write-Host ""
Write-Host "Done! mind-map is ready to use." -ForegroundColor Green
Write-Host ""
Write-Host "MCP clients are configured to launch the server via WSL:" -ForegroundColor DarkGray
Write-Host "  command: wsl" -ForegroundColor DarkGray
Write-Host "  args:    [$WslBinaryPath, serve, --stdio]" -ForegroundColor DarkGray
Write-Host ""

# ---------------------------------------------------------------------------
# 6. Interactive: set up as a persistent service
# ---------------------------------------------------------------------------

$DefaultPort = "51849"
$DefaultWikiDir = "~/.mind-map/wiki"

Write-Host ""
$installService = Read-Host "Would you like to install mind-map as a persistent service? [y/N]"

if ($installService -match '^[Yy]$') {
    $servicePort = Read-Host "Port [$DefaultPort]"
    if ([string]::IsNullOrWhiteSpace($servicePort)) { $servicePort = $DefaultPort }

    $serviceWikiDir = Read-Host "Wiki directory [$DefaultWikiDir]"
    if ([string]::IsNullOrWhiteSpace($serviceWikiDir)) { $serviceWikiDir = $DefaultWikiDir }

    # Create wiki directory inside WSL
    wsl -d $WslDistro bash -c "mkdir -p $($serviceWikiDir -replace '~','`$HOME')" 2>&1 | Out-Null

    # Create a Scheduled Task that launches the server via WSL at logon
    $taskName = "mind-map"
    $taskAction = New-ScheduledTaskAction `
        -Execute "wsl.exe" `
        -Argument "-d $WslDistro $WslBinaryPath serve --addr :$servicePort --dir $serviceWikiDir"

    $taskTrigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    $taskSettings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -RestartCount 3 `
        -RestartInterval ([TimeSpan]::FromMinutes(1))

    # Remove existing task if present
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue

    Register-ScheduledTask `
        -TaskName $taskName `
        -Action $taskAction `
        -Trigger $taskTrigger `
        -Settings $taskSettings `
        -Description "mind-map wiki server (via WSL)" | Out-Null

    # Start it now
    Start-ScheduledTask -TaskName $taskName

    Write-Host ""
    Write-Ok "Installed and started scheduled task '$taskName'"
    Write-Host "    Status: Get-ScheduledTask -TaskName '$taskName'" -ForegroundColor DarkGray
    Write-Host "    Stop:   Stop-ScheduledTask -TaskName '$taskName'" -ForegroundColor DarkGray
    Write-Host "    Remove: Unregister-ScheduledTask -TaskName '$taskName'" -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "  Web UI:       http://localhost:$servicePort" -ForegroundColor Cyan
    Write-Host "  MCP endpoint: http://localhost:$servicePort/mcp" -ForegroundColor Cyan
} else {
    Write-Host "Start the wiki server (from WSL):" -ForegroundColor DarkGray
    Write-Host "  mind-map serve --dir ~/.mind-map/wiki" -ForegroundColor DarkGray
}
