# Install ai-workflow.exe from a GitHub release after SHA-256 verification.
# Usage: irm https://raw.githubusercontent.com/Taki7980/Ai-workflow-V3/main/install.ps1 | iex
# Env: AI_WORKFLOW_VERSION=X.Y.Z (default latest), AI_WORKFLOW_INSTALL_DIR
$ErrorActionPreference = 'Stop'
$repo = 'Taki7980/Ai-workflow-V3'
$installDir = if ($env:AI_WORKFLOW_INSTALL_DIR) { $env:AI_WORKFLOW_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\ai-workflow' }

if ($env:AI_WORKFLOW_VERSION) {
    $version = $env:AI_WORKFLOW_VERSION.TrimStart('v')
} else {
    $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name.TrimStart('v')
}
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw "Unable to resolve a stable release version (got: $version)" }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}
$asset = "ai-workflow-v$version-windows-$arch.exe"
$base = "https://github.com/$repo/releases/download/v$version"
if ($env:AI_WORKFLOW_TEST_RELEASE_BASE) {
    if ($env:AI_WORKFLOW_TEST_RELEASE_BASE -notmatch '^http://(127\.0\.0\.1|localhost):\d+$') { throw 'AI_WORKFLOW_TEST_RELEASE_BASE is restricted to localhost HTTP' }
    $base = $env:AI_WORKFLOW_TEST_RELEASE_BASE
}

$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Invoke-WebRequest "$base/SHA256SUMS" -OutFile (Join-Path $tmp 'SHA256SUMS') -UseBasicParsing
    Invoke-WebRequest "$base/$asset" -OutFile (Join-Path $tmp $asset) -UseBasicParsing
    $line = Get-Content (Join-Path $tmp 'SHA256SUMS') | Where-Object { ($_ -split '\s+\*?')[1] -eq $asset } | Select-Object -First 1
    if (-not $line) { throw "No SHA-256 checksum listed for $asset" }
    $expected = ($line -split '\s+')[0].ToLowerInvariant()
    $actual = (Get-FileHash (Join-Path $tmp $asset) -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($expected -ne $actual) { throw "SHA-256 verification failed for $asset" }

    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Move-Item -Force (Join-Path $tmp $asset) (Join-Path $installDir 'ai-workflow.exe')
} finally {
    Remove-Item -Recurse -Force $tmp
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $installDir) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    Write-Host "Added $installDir to your user PATH (restart the terminal)."
}
Write-Host "Installed ai-workflow $version to $installDir\ai-workflow.exe (SHA-256 verified)"
Write-Host "Verify build provenance: gh attestation verify `"$installDir\ai-workflow.exe`" --repo $repo"
