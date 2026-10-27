# Inject the agent API key from the local pi config into this process,
# then start chatmod. The key is never written to the repo or to disk.
#
#   .\scripts\run-with-agent.ps1
#   .\scripts\run-with-agent.ps1 -Provider "Opencode Go" -Model deepseek-v4.1-flash
#   $env:ADDR=":9000"; .\scripts\run-with-agent.ps1
#
# Manual alternative:
#   $env:AGENT_API_KEY = "new key"
#   go run .
param(
    [string]$ModelsPath = (Join-Path $HOME ".pi\agent\models.json"),
    [string]$Provider = "Opencode Go",
    [string]$Model = ""
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path -LiteralPath $ModelsPath)) {
    throw "models.json not found: $ModelsPath (use -ModelsPath to override)"
}
$cfg = Get-Content -LiteralPath $ModelsPath -Raw -Encoding UTF8 | ConvertFrom-Json
$provider = $cfg.providers.PSObject.Properties[$Provider]
if (-not $provider) {
    $names = ($cfg.providers.PSObject.Properties | ForEach-Object { $_.Name }) -join ", "
    throw "provider '$Provider' not found in models.json. Available: $names"
}

$key = [string]$provider.Value.apiKey
if ([string]::IsNullOrWhiteSpace($key)) {
    throw "provider '$Provider' has no apiKey"
}

$env:AGENT_API_KEY = $key.Trim()
if (-not $env:AGENT_BASE_URL -and $provider.Value.baseUrl) {
    $env:AGENT_BASE_URL = [string]$provider.Value.baseUrl
}
if ($Model) {
    $env:AGENT_MODEL = $Model
}

Write-Host "agent: provider=$Provider model=$($env:AGENT_MODEL) base=$($env:AGENT_BASE_URL) key=***"
try {
    # Remaining args are passed to `go run`. ADDR/DB_PATH still come from env vars.
    & go run . @args
    exit $LASTEXITCODE
}
finally {
    Remove-Item Env:\AGENT_API_KEY -ErrorAction SilentlyContinue
}
