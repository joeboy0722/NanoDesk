param(
    [Parameter(Mandatory=$true)][string]$AppPath,
    [Parameter(Mandatory=$true)][string]$TraceDirectory
)
$ErrorActionPreference = 'Stop'
$app = (Resolve-Path -LiteralPath $AppPath).Path
$previous = $env:NANODESK_STREAM_TRACE_DIR
try {
    $env:NANODESK_STREAM_TRACE_DIR = [IO.Path]::GetFullPath($TraceDirectory)
    New-Item -ItemType Directory -Path $env:NANODESK_STREAM_TRACE_DIR -Force | Out-Null
    $process = Start-Process -FilePath $app -WorkingDirectory ([IO.Path]::GetDirectoryName($app)) -WindowStyle Normal -PassThru
    Write-Output ('Started diagnostic process {0}; traces: {1}' -f $process.Id,$env:NANODESK_STREAM_TRACE_DIR)
} finally {
    $env:NANODESK_STREAM_TRACE_DIR = $previous
}
