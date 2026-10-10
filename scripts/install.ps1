# Copyright 2026 The Shiplino Authors
# SPDX-License-Identifier: FSL-1.1-ALv2
#
# Shiplino installer (Windows, PowerShell 5.1+).
#
#   irm https://raw.githubusercontent.com/elephaant/shiplino/main/scripts/install.ps1 | iex
#
# Downloads the release for this CPU, verifies its SHA-256 checksum,
# installs to %USERPROFILE%\.shiplino\bin and runs `shiplino setup`.
# Environment: SHIPLINO_VERSION, SHIPLINO_HOME, SHIPLINO_NO_SETUP=1,
# SHIPLINO_DOWNLOAD_BASE (see install.sh).

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$repo = 'elephaant/shiplino'

function Fail($msg) { Write-Error "shiplino install: $msg"; exit 1 }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) { 'AMD64' { 'amd64' } 'ARM64' { 'arm64' } default { Fail "unsupported CPU $env:PROCESSOR_ARCHITECTURE" } }

$version = $env:SHIPLINO_VERSION
if (-not $version) {
  $releases = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases?per_page=1" -UseBasicParsing
  if (-not $releases) { Fail 'no release found' }
  $version = $releases[0].tag_name
}
$num = $version.TrimStart('v')
$base = if ($env:SHIPLINO_DOWNLOAD_BASE) { $env:SHIPLINO_DOWNLOAD_BASE } else { "https://github.com/$repo/releases/download/$version" }
$archive = "shiplino_${num}_windows_${arch}.zip"

$work = Join-Path ([IO.Path]::GetTempPath()) ("shiplino-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $work | Out-Null
try {
  Write-Host "Downloading Shiplino $version for windows/$arch..."
  Invoke-WebRequest -Uri "$base/$archive" -OutFile (Join-Path $work $archive) -UseBasicParsing
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $work 'checksums.txt') -UseBasicParsing

  $line = Get-Content (Join-Path $work 'checksums.txt') | Where-Object { $_ -match " $([regex]::Escape($archive))$" } | Select-Object -First 1
  if (-not $line) { Fail "$archive is not listed in checksums.txt" }
  $want = ($line -split ' ')[0].ToLower()
  $got = (Get-FileHash -Algorithm SHA256 (Join-Path $work $archive)).Hash.ToLower()
  if ($want -ne $got) { Fail "checksum mismatch for $archive (expected $want, got $got); not installing" }
  Write-Host 'Checksum verified.'

  Expand-Archive -Path (Join-Path $work $archive) -DestinationPath (Join-Path $work 'x') -Force
  $home_ = if ($env:SHIPLINO_HOME) { $env:SHIPLINO_HOME } else { Join-Path $env:USERPROFILE '.shiplino' }
  $bin = Join-Path $home_ 'bin'
  New-Item -ItemType Directory -Path $bin -Force | Out-Null
  $dest = Join-Path $bin 'shiplino.exe'
  Copy-Item (Join-Path $work 'x\shiplino.exe') "$dest.new" -Force
  Move-Item "$dest.new" $dest -Force
  Write-Host "Installed $(& $dest version) to $dest"

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (($userPath -split ';') -notcontains $bin) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$bin", 'User')
    Write-Host "Added $bin to your PATH (new terminals)."
  }
} finally {
  Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

if ($env:SHIPLINO_NO_SETUP -eq '1') { Write-Host "Skipping setup. Run: $dest setup"; exit 0 }
& $dest setup
