param(
    [string]$Version
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repo = (Resolve-Path "$PSScriptRoot/../..").Path
Set-Location -LiteralPath $repo

if (!$Version) { $Version = (Get-Content -Raw -LiteralPath 'frontend/package.json' | ConvertFrom-Json).version }
$Version = $Version -creplace '^v', ''
if ($Version -cnotmatch '^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$') { throw "Invalid application version: $Version" }

$package = (New-Item -ItemType Directory -Path 'build/Shell-Windows-x64' -Force).FullName
$releaseDir = (New-Item -ItemType Directory -Path 'build/release' -Force).FullName
$timer = [Diagnostics.Stopwatch]::StartNew()

# Generate Shell.ico and Windows COFF resources (.syso) in pure Go (no MSVC cvtres.exe needed).
go run ./cmd/genrsrc -version $Version -out rsrc_windows_amd64.syso
if ($LASTEXITCODE) { throw 'Windows resource generation failed.' }

$tags = 'desktop,production,wv2runtime.download'
$ldflags = "-s -w -H=windowsgui -X main.appVersion=$Version"
$app = Join-Path $package 'Shell.exe'
go build -trimpath -tags $tags -ldflags $ldflags -o $app .
if ($LASTEXITCODE) { throw 'Go build failed.' }
$timer.Stop()

# Verify Windows x64 GUI PE header and embedded version info.
$pe = [IO.File]::ReadAllBytes($app)
$header = [BitConverter]::ToInt32($pe, 0x3c)
if ([BitConverter]::ToUInt32($pe, $header) -ne 0x4550 -or
    [BitConverter]::ToUInt16($pe, $header + 4) -ne 0x8664 -or
    [BitConverter]::ToUInt16($pe, $header + 24 + 68) -ne 2) {
    throw 'Expected a Windows x64 GUI executable.'
}
$resource = (Get-Item -LiteralPath $app).VersionInfo
$expectedFixed = ($Version -split '[-+]')[0] + '.0'
if ($resource.ProductName -cne 'Shell' -or $resource.FileVersion -cne $Version -or $resource.ProductVersion -cne $Version -or
    $resource.FileVersionRaw.ToString() -ne $expectedFixed -or $resource.ProductVersionRaw.ToString() -ne $expectedFixed) {
    throw "Windows FileVersion and ProductVersion must match $Version (fixed resource $expectedFixed)."
}

Copy-Item -LiteralPath README.md -Destination $package -Force
if (Test-Path LICENSE) {
    Copy-Item -LiteralPath LICENSE -Destination $package -Force
}
if (Test-Path LICENSES) {
    Copy-Item -LiteralPath LICENSES -Destination $package -Recurse -Force
}

$licenseDir = (New-Item -Path "$package/LICENSES/Go" -ItemType Directory -Force).FullName
$goRoot = go env GOROOT
Copy-Item -LiteralPath (Join-Path $goRoot 'LICENSE') -Destination "$licenseDir/Go-LICENSE.txt" -Force

# Record notices for the Go modules actually linked into Shell.exe.
$records = @(go list -tags $tags -deps -f '{{if and .Module (not .Module.Main)}}{{.Module.Path}}|{{.Module.Dir}}|{{.Dir}}{{end}}' .)
if ($LASTEXITCODE) { throw 'Could not determine linked Go dependencies.' }
$seen = @{}
foreach ($record in $records) {
    if (!$record) { continue }
    $module, $root, $dir = $record.Split('|')
    while (!$seen.ContainsKey($dir)) {
        $seen[$dir] = $true
        $notices = @(Get-ChildItem -LiteralPath $dir -File -ErrorAction SilentlyContinue | Where-Object Name -Match '^(LICENSE|LICENCE|COPYING|NOTICE|PATENTS)([._-].*)?$')
        if ($notices.Count) {
            $relative = if ($dir -eq $root) { '.' } else { $dir.Substring($root.Length).TrimStart('\', '/') }
            $noticeDir = (New-Item -ItemType Directory -Force -Path (Join-Path (Join-Path $licenseDir $module) $relative)).FullName
            foreach ($notice in $notices) { Copy-Item -LiteralPath $notice.FullName -Destination $noticeDir -Force }
        }
        if ($dir -eq $root) { break }
        $dir = Split-Path -Parent $dir
    }
}
go version -m $app | Set-Content -LiteralPath "$licenseDir/modules.txt" -Encoding utf8
if ($LASTEXITCODE) { throw 'Could not read the binary module inventory.' }

# Produce standalone .exe, portable .zip, and SHA256SUMS.txt for GitHub Release.
$standaloneExe = Join-Path $releaseDir "Shell-Windows-x64.exe"
$zipFile = Join-Path $releaseDir "Shell-Windows-x64.zip"
Copy-Item -LiteralPath $app -Destination $standaloneExe -Force
if (Test-Path $zipFile) { Remove-Item -LiteralPath $zipFile -Force }
Compress-Archive -Path "$package/*" -DestinationPath $zipFile -CompressionLevel Optimal

$exeHash = (Get-FileHash -LiteralPath $standaloneExe -Algorithm SHA256).Hash.ToLowerInvariant()
$zipHash = (Get-FileHash -LiteralPath $zipFile -Algorithm SHA256).Hash.ToLowerInvariant()
@(
    "$exeHash  Shell-Windows-x64.exe"
    "$zipHash  Shell-Windows-x64.zip"
) | Set-Content -LiteralPath (Join-Path $releaseDir 'SHA256SUMS.txt') -Encoding utf8

$size = (Get-ChildItem -LiteralPath $package -Recurse -File | Measure-Object -Property Length -Sum).Sum
$summary = "Windows release build (v$Version): $([math]::Round($timer.Elapsed.TotalSeconds, 2)) s. Shell.exe: $([math]::Round($pe.Length / 1MB, 2)) MB ($($pe.Length) bytes). Package: $size bytes. SHA-256: $exeHash"
Write-Host $summary
if ($env:GITHUB_STEP_SUMMARY) {
    $summary | Out-File -FilePath $env:GITHUB_STEP_SUMMARY -Append -Encoding utf8
}

# Reference sizes are the packaged Windows assets in haoch1/Shell v1.5.5.
$releaseBaseline = @{ 'Shell-Windows-x64.exe' = 16592896; 'Shell-Windows-x64.zip' = 6460635 }
foreach ($name in $releaseBaseline.Keys) {
    $bytes = (Get-Item -LiteralPath (Join-Path $releaseDir $name)).Length
    $baseline = $releaseBaseline[$name]
    $growth = ($bytes - $baseline) * 100.0 / $baseline
    $line = '{0}  {1:N0} bytes ({2:+0.00;-0.00;0.00}% vs v1.5.5)' -f $name, $bytes, $growth
    Write-Host $line
    if ($env:GITHUB_STEP_SUMMARY) {
        $line | Out-File -FilePath $env:GITHUB_STEP_SUMMARY -Append -Encoding utf8
    }
    if ($bytes -gt $baseline * 1.05) { throw "$name exceeds the 5% size growth budget against v1.5.5." }
}
