[CmdletBinding()]
param(
    [string]$Version = $(if ($env:STACKER_VERSION) { $env:STACKER_VERSION } else { 'latest' }),
    [string]$InstallDir = $env:STACKER_INSTALL_DIR,
    [string]$SourceDir,
    [string]$ReleaseBaseUrl = 'https://github.com/tarcisiomiranda/stacker/releases',
    [switch]$NoPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw 'This installer requires Windows.'
}
$architecture = $env:PROCESSOR_ARCHITEW6432
if (-not $architecture) { $architecture = $env:PROCESSOR_ARCHITECTURE }
if ($architecture -ne 'AMD64') {
    throw "Only Windows x64 is supported; detected $architecture."
}
if ($Version -ne 'latest' -and $Version -notmatch '^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$') {
    throw 'Version must be latest or a SemVer tag such as v1.2.3.'
}
if (-not $InstallDir) {
    $InstallDir = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Programs\stacker\bin'
}
$InstallDir = [IO.Path]::GetFullPath($InstallDir)
$destination = Join-Path $InstallDir 'stacker.exe'
$asset = 'stacker-windows-amd64.exe'
$baseUrl = $ReleaseBaseUrl.TrimEnd('/')
if ($Version -eq 'latest') {
    $downloadUrl = "$baseUrl/latest/download"
} else {
    $downloadUrl = "$baseUrl/download/$Version"
}
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
[IO.Directory]::CreateDirectory($InstallDir) | Out-Null
$staging = Join-Path $InstallDir ('.install-' + [Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($staging) | Out-Null
$stagedBinary = Join-Path $staging $asset
$stagedChecksums = Join-Path $staging 'checksums.txt'
$stagedBackup = Join-Path $staging 'previous.exe'

try {
    if ($SourceDir) {
        Copy-Item -LiteralPath (Join-Path $SourceDir 'checksums.txt') -Destination $stagedChecksums
    } else {
        Invoke-WebRequest -UseBasicParsing -Uri "$downloadUrl/checksums.txt" -OutFile $stagedChecksums
    }
    $checksumText = [IO.File]::ReadAllText($stagedChecksums)
    $pattern = '(?m)^([a-fA-F0-9]{64})[ \t]+\*?' + [regex]::Escape($asset) + '\r?$'
    $entries = [regex]::Matches($checksumText, $pattern)
    if ($entries.Count -ne 1) {
        throw "checksums.txt must contain exactly one SHA-256 entry for $asset."
    }
    if ($SourceDir) {
        Copy-Item -LiteralPath (Join-Path $SourceDir $asset) -Destination $stagedBinary
    } else {
        Invoke-WebRequest -UseBasicParsing -Uri "$downloadUrl/$asset" -OutFile $stagedBinary
    }
    $actual = (Get-FileHash -LiteralPath $stagedBinary -Algorithm SHA256).Hash
    if ($actual -ne $entries[0].Groups[1].Value) {
        throw "SHA-256 mismatch for $asset; the previous installation was preserved."
    }
    if ([IO.File]::Exists($destination)) {
        try {
            [IO.File]::Replace($stagedBinary, $destination, $stagedBackup)
        } catch {
            throw "Cannot update $destination. Close processes using the executable and retry. $($_.Exception.Message)"
        }
    } else {
        [IO.File]::Move($stagedBinary, $destination)
    }
} finally {
    if ([IO.File]::Exists($stagedBinary)) { [IO.File]::Delete($stagedBinary) }
    if ([IO.File]::Exists($stagedChecksums)) { [IO.File]::Delete($stagedChecksums) }
    if ([IO.File]::Exists($stagedBackup)) { [IO.File]::Delete($stagedBackup) }
    [IO.Directory]::Delete($staging)
}

if (-not $NoPath) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $userEntries = @($userPath -split ';' | Where-Object { $_ })
    $normalizedEntries = @($userEntries | ForEach-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') })
    if ($normalizedEntries -notcontains $InstallDir.TrimEnd('\')) {
        [Environment]::SetEnvironmentVariable('Path', (($userEntries + $InstallDir) -join ';'), 'User')
    }
    $currentEntries = @($env:Path -split ';' | ForEach-Object { $_.TrimEnd('\') })
    if ($currentEntries -notcontains $InstallDir.TrimEnd('\')) {
        $env:Path = $InstallDir + ';' + $env:Path
    }
}
Write-Output "Installed stacker: $destination"
if (-not $NoPath) { Write-Output 'Open a new terminal to use the updated user PATH.' }
