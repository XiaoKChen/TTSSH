# Builds ttssh and installs it to a directory on the user PATH (Windows).
#
#   .\scripts\install.ps1              build from source, then install
#   .\scripts\install.ps1 -NoBuild     install an already-built binary
#                                      (.\ttssh.exe or dist\ttssh-windows-amd64.exe)
#
# Installs to %LOCALAPPDATA%\Programs\ttssh and adds that folder to the
# user PATH (no admin rights needed). Open a new terminal after the first
# install for the PATH change to take effect.

param(
    [switch]$NoBuild
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$installDir = Join-Path $env:LOCALAPPDATA 'Programs\ttssh'
$target = Join-Path $installDir 'ttssh.exe'

New-Item -ItemType Directory -Force $installDir | Out-Null

if ($NoBuild) {
    $candidates = @(
        (Join-Path $repoRoot 'ttssh.exe'),
        (Join-Path $repoRoot 'dist\ttssh-windows-amd64.exe')
    )
    $source = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
    if (-not $source) {
        Write-Error "No prebuilt binary found (looked for ttssh.exe and dist\ttssh-windows-amd64.exe). Run without -NoBuild to build from source."
    }
    Copy-Item $source $target -Force
    Write-Host "Installed $source -> $target"
} else {
    go build -o $target "$repoRoot\cmd\ttssh"
    if ($LASTEXITCODE -ne 0) { Write-Error "go build failed" }
    Write-Host "Built and installed -> $target"
}

# Add the install dir to the user PATH if it is not already there.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$onPath = ($userPath -split ';' | Where-Object { $_ }) -contains $installDir
if (-not $onPath) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    Write-Host "Added $installDir to your user PATH."
    Write-Host "Open a NEW terminal, then 'ttssh' will work from anywhere."
} else {
    Write-Host "$installDir is already on your PATH — 'ttssh' works from anywhere."
}
