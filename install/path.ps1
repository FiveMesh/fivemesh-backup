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
