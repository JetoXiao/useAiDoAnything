param(
    [int]$Iterations = 20,
    [int]$DurationMinutes = 0
)

$ErrorActionPreference = 'Stop'

$workspaceRoot = Split-Path -Parent $PSScriptRoot
$backendRoot = Join-Path $workspaceRoot 'backend'
$startedAt = Get-Date
$deadline = if ($DurationMinutes -gt 0) { $startedAt.AddMinutes($DurationMinutes) } else { $null }
$completed = 0

Push-Location $backendRoot
try {
    Write-Host 'Compiling scheduler-related service and handler packages...'
    go test ./internal/service ./internal/handler -tags=unit -run '^$' -count=1
    if ($LASTEXITCODE -ne 0) { throw 'Go package compilation failed.' }

    while ($true) {
        if ($deadline) {
            if ((Get-Date) -ge $deadline -and $completed -gt 0) { break }
        } elseif ($completed -ge $Iterations) {
            break
        }

        $round = $completed + 1
        Write-Host "Scheduler regression round $round started at $((Get-Date).ToString('HH:mm:ss'))"

        go test ./internal/service -tags=unit -run 'TestRateLimitService_HandleUpstreamError_OpenAI403|TestCalculateOpenAI429ResetTime_StandardHeaders|TestShouldStopOpenAIOAuth429Failover' -count=1
        if ($LASTEXITCODE -ne 0) { throw "Service regression failed in round $round." }

        go test ./internal/handler -tags=unit -run 'TestHandleFailoverError_SameAccountRetry' -count=1
        if ($LASTEXITCODE -ne 0) { throw "Handler regression failed in round $round." }

        $completed++
    }
}
finally {
    Pop-Location
}

$elapsed = (Get-Date) - $startedAt
Write-Host "Scheduler regression passed: $completed rounds, elapsed $($elapsed.ToString())."
