# Run on the test machine with NanoDesk already running. No workload is generated.
param(
    [int]$ProcessId = 0,
    [int]$Minutes = 30,
    [int]$IntervalSeconds = 5,
    [string]$OutputDirectory = (Join-Path (Get-Location) 'nanodesk-diagnostics')
)
$ErrorActionPreference = 'Stop'
if ($Minutes -lt 1 -or $IntervalSeconds -lt 1) { throw 'Invalid duration or interval' }
if ($ProcessId -eq 0) {
    $candidates = @(Get-Process -Name NanoDesk -ErrorAction SilentlyContinue)
    if ($candidates.Count -ne 1) { throw 'Specify -ProcessId when zero or multiple NanoDesk processes are running' }
    $ProcessId = $candidates[0].Id
}
$target = Get-Process -Id $ProcessId
if ($target.ProcessName -ne 'NanoDesk') { throw 'Target must be NanoDesk' }
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class NanoDeskResourceQuery {
    [DllImport("user32.dll")] public static extern uint GetGuiResources(IntPtr process, uint flags);
}
'@
$start = Get-Date
$resourceFile = Join-Path $OutputDirectory 'resources.csv'
$eventFile = Join-Path $OutputDirectory 'power-events.csv'
try {
    while ((Get-Date) -lt $start.AddMinutes($Minutes)) {
        $target.Refresh()
        if ($target.HasExited) { Write-Warning 'NanoDesk exited'; break }
        [pscustomobject]@{
            Timestamp = (Get-Date).ToString('o')
            ProcessId = $ProcessId
            PrivateBytes = $target.PrivateMemorySize64
            WorkingSet = $target.WorkingSet64
            Handles = $target.HandleCount
            Threads = $target.Threads.Count
            GDIObjects = [NanoDeskResourceQuery]::GetGuiResources($target.Handle, 0)
            USERObjects = [NanoDeskResourceQuery]::GetGuiResources($target.Handle, 1)
            CPUSeconds = $target.TotalProcessorTime.TotalSeconds
        } | Export-Csv -LiteralPath $resourceFile -NoTypeInformation -Append -Encoding UTF8
        Start-Sleep -Seconds $IntervalSeconds
    }
} finally {
    try {
        Get-WinEvent -FilterHashtable @{LogName='System'; StartTime=$start.AddMinutes(-5); Id=41,42,107,6008,1001,4101} -ErrorAction Stop |
            Select-Object TimeCreated, Id, ProviderName, Message |
            Export-Csv -LiteralPath $eventFile -NoTypeInformation -Encoding UTF8
    } catch { Write-Warning "System events unavailable or absent: $($_.Exception.Message)" }
    $target.Dispose()
    Write-Host "Resource samples: $resourceFile"
    Write-Host "Power/crash/display events, when available: $eventFile"
}
