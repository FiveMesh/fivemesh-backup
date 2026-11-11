#Requires -Version 5.1

<#
.SYNOPSIS
Runs FiveMesh setup/configure/schedule commands with a secure Windows password
for unattended Task Scheduler mode over PowerShell Direct.

.NOTES
The password is read through PowerShell's native SecureString prompt and passed
to a one-shot, current-user-only named pipe. It is not written to argv,
environment variables, files, history, or logs.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $Executable,

    [Parameter(Mandatory = $true, ValueFromRemainingArguments = $true)]
    [string[]] $BackupArguments
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path -LiteralPath $Executable -PathType Leaf)) {
    throw "FiveMesh Backup executable not found: $Executable"
}
if ($BackupArguments.Count -eq 0) {
    throw "Provide a FiveMesh command after -Executable, for example: configure"
}

$pipeName = "fivemesh-secure-" + [guid]::NewGuid().ToString("N")
$securePassword = Read-Host -AsSecureString -Prompt "Windows password for unattended Task Scheduler execution"
if ($securePassword -isnot [System.Security.SecureString]) {
    throw "PowerShell did not provide a SecureString; refusing insecure input."
}
if ($securePassword.Length -eq 0) {
    $securePassword.Dispose()
    throw "Windows task password must not be empty."
}

$broker = $null
$bstr = [IntPtr]::Zero
$buffer = $null
$cliExit = 1
try {
    $startInfo = New-Object Diagnostics.ProcessStartInfo
    $startInfo.FileName = $Executable
    $startInfo.Arguments = "password-broker --pipe `"$pipeName`" --timeout 30m"
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardInput = $true
    $startInfo.CreateNoWindow = $true
    $broker = [Diagnostics.Process]::Start($startInfo)

    # SecureStringToBSTR produces UTF-16LE bytes without creating a managed
    # plaintext string. The BSTR length excludes its trailing NUL.
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($securePassword)
    $byteLength = [Runtime.InteropServices.Marshal]::ReadInt32($bstr, -4)
    $buffer = New-Object byte[] $byteLength
    [Runtime.InteropServices.Marshal]::Copy($bstr, $buffer, 0, $byteLength)
    $broker.StandardInput.BaseStream.Write($buffer, 0, $byteLength)
    $broker.StandardInput.BaseStream.Flush()
    $broker.StandardInput.Close()

    $arguments = @($BackupArguments) + @("--windows-password-pipe", $pipeName)
    & $Executable @arguments
    $cliExit = $LASTEXITCODE
}
finally {
    if ($null -ne $buffer) {
        [Array]::Clear($buffer, 0, $buffer.Length)
    }
    if ($bstr -ne [IntPtr]::Zero) {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
    }
    if ($null -ne $securePassword) {
        $securePassword.Dispose()
    }
    if ($null -ne $broker -and -not $broker.HasExited) {
        if (-not $broker.WaitForExit(2000)) {
            $broker.Kill()
            [void] $broker.WaitForExit(5000)
        }
    }
}

exit $cliExit
