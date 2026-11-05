param()
$ErrorActionPreference = 'Stop'
$previousCache = $env:GOCACHE
Push-Location -LiteralPath $PSScriptRoot
try {
    if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path ([IO.Path]::GetTempPath()) 'nanodesk-go-build' }
    & windres -F pe-x86-64 -i app.rc -o web/app_windows_amd64.syso
    if ($LASTEXITCODE -ne 0) { throw 'Resource compilation failed' }
    Copy-Item -LiteralPath web/app_windows_amd64.syso -Destination app/app_windows_amd64.syso
    Copy-Item -LiteralPath web/app_windows_amd64.syso -Destination server/app_windows_amd64.syso
    $guiFlags = "-H windowsgui -s -w -extldflags '-static'"
    & go build -buildvcs=false -ldflags $guiFlags -o NanoDesk-Web.exe ./web
    if ($LASTEXITCODE -ne 0) { throw 'Web build failed' }
    if ((Get-Item -LiteralPath NanoDesk-Web.exe).Length -ge 8000000) { throw 'Web EXE exceeds the 8,000,000-byte limit' }
    & go build -buildvcs=false -ldflags $guiFlags -o NanoDesk-App.exe ./app
    if ($LASTEXITCODE -ne 0) { throw 'APP build failed' }
    & go build -buildvcs=false -ldflags '-H windowsgui -s -w' -o NanoDesk-Server.exe ./server
    if ($LASTEXITCODE -ne 0) { throw 'Server build failed' }
    $report = @('NanoDesk release builds; sizes are bytes; APP target is 8,000,000 bytes.', '')
    foreach ($name in @('NanoDesk-Web.exe', 'NanoDesk-App.exe', 'NanoDesk-Server.exe')) {
        $file = Get-Item -LiteralPath $name
        $hash = (Get-FileHash -LiteralPath $name -Algorithm SHA256).Hash
        $report += ('{0}: {1} bytes; SHA256 {2}' -f $name, $file.Length, $hash)
    }
    $report | Set-Content -LiteralPath BUILD-SIZES.txt -Encoding utf8
    $report | Write-Output
} finally {
    $env:GOCACHE = $previousCache
    Pop-Location
}
