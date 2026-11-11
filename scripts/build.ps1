# Builds a laman release into dist\: laman.exe, laman-setup-<version>.exe and
# checksums.txt (SHA-256). The GitHub release workflow runs this too.
#
#   .\scripts\build.ps1 -Version 0.2.0
#   .\scripts\build.ps1 -Version 0.2.0 -NoInstaller   # skip Inno Setup
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [switch]$NoInstaller
)
$ErrorActionPreference = 'Stop'
$Version = $Version.TrimStart('v')
if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "Version must look like 1.2.3, not '$Version'" }

$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $dist = Join-Path $root 'dist'
    if (Test-Path $dist) { Remove-Item -Recurse -Force $dist }
    New-Item -ItemType Directory $dist | Out-Null

    # Version info and icon inside the exe, then the exe itself.
    go run winres/gen.go $Version
    if ($LASTEXITCODE) { throw 'winres failed' }
    $env:CGO_ENABLED = '0'
    go build -trimpath -ldflags "-s -w -X laman/internal/version.Version=$Version" -o "$dist\laman.exe" .
    if ($LASTEXITCODE) { throw 'go build failed' }
    $built = & "$dist\laman.exe" version
    if ($built -ne "laman $Version") { throw "built exe says '$built'" }

    if (-not $NoInstaller) {
        $iscc = @(
            (Get-Command ISCC.exe -ErrorAction SilentlyContinue).Source,
            "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
            "$env:ProgramFiles\Inno Setup 6\ISCC.exe",
            "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe"
        ) | Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
        if (-not $iscc) { throw 'Inno Setup 6 (ISCC.exe) not found; install it or pass -NoInstaller' }
        & $iscc /Q "/DVersion=$Version" "/DExe=$dist\laman.exe" installer\laman.iss
        if ($LASTEXITCODE) { throw 'ISCC failed' }
    }

    # checksums.txt in sha256sum format: "<hash>  <file>".
    $lines = Get-ChildItem $dist -File | Where-Object Name -ne 'checksums.txt' | Sort-Object Name | ForEach-Object {
        '{0}  {1}' -f (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower(), $_.Name
    }
    [IO.File]::WriteAllText((Join-Path $dist 'checksums.txt'), ($lines -join "`n") + "`n")
    Get-ChildItem $dist | Format-Table Name, Length -AutoSize
}
finally {
    # Put the committed (0.0.0-neutral) resources back for local builds.
    git checkout -- rsrc_windows_amd64.syso 2>$null
    Pop-Location
}
