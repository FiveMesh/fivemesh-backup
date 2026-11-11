#Requires -Version 5.1

# FiveMesh Backup installer for Windows.
#
#   irm https://github.com/FiveMesh/fivemesh-backup/releases/latest/download/install.ps1 | iex
#
# Downloads the release binary, verifies it against the release's SHA256SUMS,
# installs it for the current user, and starts guided setup. Re-run to upgrade.

[CmdletBinding()]
param(
    # Install a local build instead of downloading (requires -Sha256).
    [string] $BinaryPath,
    # Release mirror with the GitHub Releases layout:
    # <base>/latest/download/<file> and <base>/download/v<version>/<file>.
    [string] $BaseUrl = $(if ($env:FIVEMESH_BACKUP_RELEASES) { $env:FIVEMESH_BACKUP_RELEASES } else { "https://github.com/FiveMesh/fivemesh-backup/releases" }),
    [string] $Version = "latest",
    [string] $Sha256,
    [string] $InstallDir = "$env:LOCALAPPDATA\FiveMesh\bin",
    [switch] $NoSetup,
    [switch] $NoPathUpdate,
    [switch] $Uninstall,
    [switch] $Yes,
    [switch] $RemoveBinary
)

$ErrorActionPreference = "Stop"
$destinationName = "fivemesh-backup.exe"
$resolvedInstallDir = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($InstallDir)
$destination = Join-Path $resolvedInstallDir $destinationName
$defaultInstallDir = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath("$env:LOCALAPPDATA\FiveMesh\bin")

function Test-SamePathEntry([string] $left, [string] $right) {
    if ([string]::IsNullOrWhiteSpace($left) -or [string]::IsNullOrWhiteSpace($right)) {
        return $false
    }
    $normalizedLeft = $left.Trim().Trim('"').TrimEnd("\")
    $normalizedRight = $right.Trim().Trim('"').TrimEnd("\")
    return [string]::Equals($normalizedLeft, $normalizedRight, [StringComparison]::OrdinalIgnoreCase)
}

function Add-PathEntry([string] $currentPath, [string] $directory) {
    $parts = @($currentPath -split ";" | Where-Object { $_ -and -not (Test-SamePathEntry $_ $directory) })
    return (@($directory) + $parts) -join ";"
}

function Remove-PathEntry([string] $currentPath, [string] $directory) {
    if ([string]::IsNullOrWhiteSpace($currentPath)) {
        return ""
    }
    $parts = @($currentPath -split ";" | Where-Object { $_ -and -not (Test-SamePathEntry $_ $directory) })
    return $parts -join ";"
}

function Add-ToUserPath([string] $directory) {
    $current = [Environment]::GetEnvironmentVariable("Path", "User")
    $updated = Add-PathEntry $current $directory
    if ($updated -ne $current) {
        [Environment]::SetEnvironmentVariable("Path", $updated, "User")
    }
    $env:Path = Add-PathEntry $env:Path $directory
}

function Remove-FromUserPath([string] $directory) {
    $current = [Environment]::GetEnvironmentVariable("Path", "User")
    $updated = Remove-PathEntry $current $directory
    if ($updated -ne $current) {
        [Environment]::SetEnvironmentVariable("Path", $updated, "User")
        Write-Host "Removed installer PATH entry: $directory"
    }
    $env:Path = Remove-PathEntry $env:Path $directory
}

function Invoke-Uninstall {
    if (-not (Test-Path -LiteralPath $destination -PathType Leaf)) {
        throw "Installed executable not found at $destination"
    }

    $arguments = @("uninstall")
    if ($Yes) {
        $arguments += "--yes"
    }
    & $destination @arguments
    if ($LASTEXITCODE -ne 0) {
        throw "FiveMesh uninstall returned exit code $LASTEXITCODE"
    }
    if ($RemoveBinary) {
        Remove-Item -LiteralPath $destination -Force
        if (-not $NoPathUpdate) {
            Remove-FromUserPath $resolvedInstallDir
        }
        Write-Host "Removed executable: $destination"
    } else {
        Write-Host "Executable preserved: $destination"
    }
    Write-Host "Configuration, credentials, history, and backup archives were preserved."
}

if ($Uninstall) {
    Invoke-Uninstall
    return
}

if ($BinaryPath -and [string]::IsNullOrWhiteSpace($Sha256)) {
    throw "-Sha256 is required with -BinaryPath so the local binary can be verified"
}
# Windows PowerShell 5.1 on older Windows Server defaults to TLS 1.0/1.1,
# which GitHub rejects.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
if (-not [Environment]::Is64BitOperatingSystem) {
    throw "FiveMesh Backup currently supports Windows amd64 only"
}
if ($env:PROCESSOR_ARCHITECTURE -notin @("AMD64", "x86")) {
    throw "Unsupported Windows architecture: $env:PROCESSOR_ARCHITECTURE"
}
if ($env:PROCESSOR_ARCHITEW6432 -and $env:PROCESSOR_ARCHITEW6432 -ne "AMD64") {
    throw "Unsupported Windows architecture: $env:PROCESSOR_ARCHITEW6432"
}

New-Item -ItemType Directory -Force -Path $resolvedInstallDir | Out-Null
$hadExistingBinary = Test-Path -LiteralPath $destination -PathType Leaf
if ($resolvedInstallDir -ne $defaultInstallDir -and -not $hadExistingBinary) {
    Write-Warning "Using a custom install path. Existing native schedules continue to reference the old executable path; rerun setup or the appropriate schedule update after moving the binary."
}
$staging = Join-Path $resolvedInstallDir (".fivemesh-install-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $staging | Out-Null
$candidate = Join-Path $staging $destinationName
$backup = Join-Path $resolvedInstallDir (".fivemesh-backup.previous-" + [guid]::NewGuid().ToString("N"))
$installed = $false

try {
    if ($BinaryPath) {
        if (-not (Test-Path -LiteralPath $BinaryPath -PathType Leaf)) {
            throw "Binary not found: $BinaryPath"
        }
        $source = (Resolve-Path -LiteralPath $BinaryPath).Path
        Copy-Item -LiteralPath $source -Destination $candidate -Force
    } else {
        $artifact = "fivemesh-backup-windows-amd64.exe"
        $base = $BaseUrl.TrimEnd("/")
        if ($base -notmatch '^(https|file)://') {
            throw "-BaseUrl must use https"
        }
        if ($Version -eq "latest") {
            $releaseUrl = "$base/latest/download"
        } else {
            $releaseUrl = "$base/download/v" + $Version.TrimStart("v")
        }
        Write-Host "Downloading $artifact ($Version)"
        $previousProgress = $ProgressPreference
        $ProgressPreference = "SilentlyContinue"  # the progress bar makes downloads far slower in 5.1
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "$releaseUrl/$artifact" -OutFile $candidate
            if ([string]::IsNullOrWhiteSpace($Sha256)) {
                $sums = (Invoke-WebRequest -UseBasicParsing -Uri "$releaseUrl/SHA256SUMS").Content
                if ($sums -is [byte[]]) {
                    $sums = [Text.Encoding]::UTF8.GetString($sums)
                }
                foreach ($line in ($sums -split "`n")) {
                    $fields = $line.Trim() -split '\s+'
                    if ($fields.Count -ge 2 -and $fields[1].TrimStart("*") -eq $artifact) {
                        $Sha256 = $fields[0]
                        break
                    }
                }
                if ([string]::IsNullOrWhiteSpace($Sha256)) {
                    throw "SHA256SUMS does not list $artifact"
                }
            }
        } finally {
            $ProgressPreference = $previousProgress
        }
    }

    $actualSha256 = (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash.ToLowerInvariant()
    $expectedSha256 = $Sha256.Trim().ToLowerInvariant()
    if ($actualSha256 -ne $expectedSha256) {
        throw "SHA-256 verification failed for $candidate"
    }

    if ($hadExistingBinary) {
        [IO.File]::Replace($candidate, $destination, $backup, $true)
    } else {
        [IO.File]::Move($candidate, $destination)
    }
    $installed = $true

    & $destination version | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Installed binary failed its version check"
    }
    if (-not $NoPathUpdate) {
        Add-ToUserPath $resolvedInstallDir
    }

    if (Test-Path -LiteralPath $backup) {
        Remove-Item -LiteralPath $backup -Force
    }
    Write-Host "Installed FiveMesh Backup at $destination"
    Write-Host "Existing configuration, schedules, credentials, history, and archives were not modified."

} catch {
    if ($installed) {
        if (Test-Path -LiteralPath $backup -PathType Leaf) {
            if (Test-Path -LiteralPath $destination -PathType Leaf) {
                Remove-Item -LiteralPath $destination -Force
            }
            [IO.File]::Move($backup, $destination)
            Write-Warning "Installation failed; the previous executable was restored."
        } elseif (Test-Path -LiteralPath $destination -PathType Leaf) {
            Remove-Item -LiteralPath $destination -Force
        }
    }
    throw
} finally {
    if (Test-Path -LiteralPath $staging) {
        Remove-Item -LiteralPath $staging -Recurse -Force
    }
}

if (-not $NoSetup) {
    & $destination setup
    if ($LASTEXITCODE -ne 0) {
        throw "FiveMesh setup returned exit code $LASTEXITCODE"
    }
}
