#Requires -Version 5.1

[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$repositoryRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot "path.ps1")
$work = Join-Path ([IO.Path]::GetTempPath()) ("fivemesh-installer-test-" + [guid]::NewGuid().ToString("N"))
$prefix = Join-Path $work "prefix"
$userData = Join-Path $work "user-data"
$previousBackupHome = $env:FIVEMESH_BACKUP_HOME
New-Item -ItemType Directory -Force -Path $work, $userData | Out-Null
$env:FIVEMESH_BACKUP_HOME = $userData

try {
    function Build-TestBinary([string] $path, [string] $version) {
        Push-Location $repositoryRoot
        try {
            $ldflags = "-s -w -X github.com/FiveMesh/fivemesh-backup/internal/version.Version=$version -X github.com/FiveMesh/fivemesh-backup/internal/version.Commit=installer-test -X github.com/FiveMesh/fivemesh-backup/internal/version.Date=2026-10-21T00:00:00Z"
            go build -trimpath -ldflags $ldflags -o $path ./cmd/fivemesh-backup
        } finally {
            Pop-Location
        }
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed with exit code $LASTEXITCODE"
        }
    }
    $candidateV1 = Join-Path $work "fivemesh-backup-v1.exe"
    $candidateV2 = Join-Path $work "fivemesh-backup-v2.exe"
    Build-TestBinary $candidateV1 "installer-test-v1"
    Build-TestBinary $candidateV2 "installer-test-v2"
    $sha256V1 = (Get-FileHash -LiteralPath $candidateV1 -Algorithm SHA256).Hash
    $sha256V2 = (Get-FileHash -LiteralPath $candidateV2 -Algorithm SHA256).Hash
    if ($sha256V1 -eq $sha256V2) {
        throw "Version-stamped test binaries are not distinguishable"
    }
    if ((Add-PathEntry "C:\one;$prefix;C:\two" $prefix) -ne "$prefix;C:\one;C:\two") {
        throw "PATH insertion helper changed unrelated entries"
    }
    if ((Remove-PathEntry "C:\one;$prefix;C:\two;C:\three" $prefix) -ne "C:\one;C:\two;C:\three") {
        throw "PATH removal helper did not remove only the exact installer entry"
    }
    if ((Remove-PathEntry "C:\one;C:\prefix-other;C:\two" $prefix) -ne "C:\one;C:\prefix-other;C:\two") {
        throw "PATH removal helper removed an unrelated prefix"
    }

    $standaloneDirectory = Join-Path $work "standalone"
    $standaloneInstaller = Join-Path $standaloneDirectory "install.ps1"
    $standalonePrefix = Join-Path $work "standalone-prefix"
    New-Item -ItemType Directory -Path $standaloneDirectory | Out-Null
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot "install.ps1") -Destination $standaloneInstaller
    & $standaloneInstaller `
        -BinaryPath $candidateV1 `
        -Sha256 $sha256V1 `
        -InstallDir $standalonePrefix `
        -NoSetup `
        -NoPathUpdate
    & (Join-Path $standalonePrefix "fivemesh-backup.exe") version | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Standalone installer output failed its version check"
    }

    & (Join-Path $PSScriptRoot "install.ps1") `
        -BinaryPath $candidateV1 `
        -Sha256 $sha256V1 `
        -InstallDir $prefix `
        -NoSetup `
        -NoPathUpdate

    $installed = Join-Path $prefix "fivemesh-backup.exe"
    $versionV1 = & $installed version
    if ($LASTEXITCODE -ne 0) {
        throw "Installed binary failed its version check"
    }
    if (($versionV1 -join "`n") -notmatch "installer-test-v1") {
        throw "Initial install did not contain version v1"
    }

    $badChecksumRejected = $false
    try {
        & (Join-Path $PSScriptRoot "install.ps1") `
            -BinaryPath $candidateV1 `
            -Sha256 "deadbeef" `
            -InstallDir (Join-Path $work "bad-prefix") `
            -NoSetup `
            -NoPathUpdate
    } catch {
        $badChecksumRejected = $true
    }
    if (-not $badChecksumRejected) {
        throw "Installer accepted an invalid checksum"
    }

    # Download path: a file:// mirror with the GitHub Releases layout.
    $artifact = "fivemesh-backup-windows-amd64.exe"
    $mirror = Join-Path $work "mirror"
    foreach ($release in @("latest/download", "download/v9.9.9")) {
        $releaseDir = Join-Path $mirror $release
        New-Item -ItemType Directory -Force -Path $releaseDir | Out-Null
        Copy-Item -LiteralPath $candidateV1 -Destination (Join-Path $releaseDir $artifact)
        Set-Content -LiteralPath (Join-Path $releaseDir "SHA256SUMS") -Value ("{0}  {1}" -f $sha256V1.ToLowerInvariant(), $artifact) -Encoding ASCII
    }
    $mirrorUrl = ([Uri] $mirror).AbsoluteUri
    foreach ($case in @(@{ Version = "latest"; Dir = "dl-latest" }, @{ Version = "9.9.9"; Dir = "dl-pinned" })) {
        $target = Join-Path $work $case.Dir
        & (Join-Path $PSScriptRoot "install.ps1") -BaseUrl $mirrorUrl -Version $case.Version -InstallDir $target -NoSetup -NoPathUpdate
        if ((Get-FileHash -LiteralPath (Join-Path $target "fivemesh-backup.exe") -Algorithm SHA256).Hash -ne $sha256V1) {
            throw "Downloaded install ($($case.Version)) does not match the release binary"
        }
    }
    Set-Content -LiteralPath (Join-Path $mirror "latest/download/SHA256SUMS") -Value ("deadbeef  " + $artifact) -Encoding ASCII
    $tamperedRejected = $false
    try {
        & (Join-Path $PSScriptRoot "install.ps1") -BaseUrl $mirrorUrl -InstallDir (Join-Path $work "dl-tampered") -NoSetup -NoPathUpdate
    } catch {
        $tamperedRejected = $true
    }
    if (-not $tamperedRejected -or (Test-Path -LiteralPath (Join-Path $work "dl-tampered/fivemesh-backup.exe"))) {
        throw "Installer accepted a download that does not match SHA256SUMS"
    }

    "test-password" | & $installed init `
        --config (Join-Path $userData "config.toml") `
        --credentials (Join-Path $userData "credentials.toml") `
        --state-dir (Join-Path $userData "state") `
        --profile production `
        --host 127.0.0.1 `
        --port 3306 `
        --user backup `
        --database fxserver `
        --backup-dir (Join-Path $userData "backups") `
        --password-stdin
    if ($LASTEXITCODE -ne 0) {
        throw "Initialization failed with exit code $LASTEXITCODE"
    }

    & (Join-Path $PSScriptRoot "install.ps1") `
        -BinaryPath $candidateV2 `
        -Sha256 $sha256V2 `
        -InstallDir $prefix `
        -NoSetup `
        -NoPathUpdate
    $versionV2 = & $installed version
    if ($LASTEXITCODE -ne 0) {
        throw "Upgraded binary failed its version check"
    }
    if (($versionV2 -join "`n") -notmatch "installer-test-v2") {
        throw "Upgrade did not install version v2"
    }
    if ((Get-FileHash -LiteralPath $installed -Algorithm SHA256).Hash -ne $sha256V2) {
        throw "Upgrade did not install the second executable"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $userData "config.toml") -PathType Leaf)) {
        throw "Upgrade modified existing configuration"
    }

    $badCandidate = Join-Path $work "verified-but-invalid.exe"
    Copy-Item -LiteralPath (Join-Path $env:SystemRoot "System32\whoami.exe") -Destination $badCandidate
    $badCandidateSha = (Get-FileHash -LiteralPath $badCandidate -Algorithm SHA256).Hash
    $rollbackVerified = $false
    try {
        & (Join-Path $PSScriptRoot "install.ps1") `
            -BinaryPath $badCandidate `
            -Sha256 $badCandidateSha `
            -InstallDir $prefix `
            -NoSetup `
            -NoPathUpdate
    } catch {
        $rollbackVerified = $true
    }
    if (-not $rollbackVerified) {
        throw "Installer accepted an executable that failed post-replacement validation"
    }
    if ((Get-FileHash -LiteralPath $installed -Algorithm SHA256).Hash -ne $sha256V2) {
        throw "Failed replacement did not restore the previous executable"
    }
    $restoredVersion = & $installed version
    if (($restoredVersion -join "`n") -notmatch "installer-test-v2") {
        throw "Rollback did not restore the runnable v2 binary"
    }

    & (Join-Path $PSScriptRoot "install.ps1") `
        -Uninstall `
        -InstallDir $prefix `
        -Yes `
        -NoPathUpdate `
        -RemoveBinary

    foreach ($required in @("config.toml", "credentials.toml")) {
        if (-not (Test-Path -LiteralPath (Join-Path $userData $required) -PathType Leaf)) {
            throw "Uninstall removed $required"
        }
    }
    if (Test-Path -LiteralPath $installed) {
        throw "Uninstall did not remove the explicitly selected executable"
    }

    Write-Host "Windows installer and uninstall checks passed."
} finally {
    if ($null -eq $previousBackupHome) {
        Remove-Item Env:FIVEMESH_BACKUP_HOME -ErrorAction SilentlyContinue
    } else {
        $env:FIVEMESH_BACKUP_HOME = $previousBackupHome
    }
    if (Test-Path -LiteralPath $work) {
        Remove-Item -LiteralPath $work -Recurse -Force
    }
}
