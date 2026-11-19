param(
    [ValidateSet('test', 'build', 'smoke')]
    [string]$Action = 'test'
)

$ErrorActionPreference = 'Stop'
$adapterRoot = $PSScriptRoot
$goDirectory = Join-Path $adapterRoot '.tools/go/bin'
$goExecutable = Join-Path $goDirectory 'go.exe'
if (!(Test-Path -LiteralPath $goExecutable)) {
    $goExecutable = (Get-Command go -ErrorAction Stop).Source
    $goDirectory = Split-Path -Parent $goExecutable
}
$env:PATH = $goDirectory + ';' + $env:PATH
$env:GOPATH = Join-Path $adapterRoot '.tools/gopath'
$env:GOCACHE = Join-Path $adapterRoot '.tools/gocache'
$env:GOTOOLCHAIN = 'local'
$version = & $goExecutable version
if ($version -notmatch 'go1\.26\.11\s') { throw "Expected Go 1.26.11, got $version" }
$upstreamRoot = Join-Path $adapterRoot 'upstream/prusa-connect'
$revision = & git -C $upstreamRoot rev-parse HEAD
if ($LASTEXITCODE -ne 0 -or $revision -ne 'b194cb83b682822e95e839a3cecf4391f3929e87') {
    throw 'The Prusa dependency must be checked out at v0.1.0-beta.1 (b194cb8).'
}

Push-Location (Join-Path $adapterRoot 'adapter')
try {
    if ($Action -eq 'test') {
        & $goExecutable test ./...
        if ($LASTEXITCODE -ne 0) { throw 'Adapter tests failed.' }
        & $goExecutable vet ./...
        if ($LASTEXITCODE -ne 0) { throw 'Go vet failed.' }
    } else {
        & $goExecutable build -o ../bin/pcma-profile.exe ./cmd/pcma-profile
        if ($LASTEXITCODE -ne 0) { throw 'Capability profile generator build failed.' }
        & $goExecutable build -o ../bin/pcma-prepare.exe ./cmd/pcma-prepare
        if ($LASTEXITCODE -ne 0) { throw 'Artifact preparation build failed.' }
        & $goExecutable build -o ../bin/moonraker-sim.exe ./cmd/moonraker-sim
        if ($LASTEXITCODE -ne 0) { throw 'Simulator build failed.' }
    }
} finally { Pop-Location }

if ($Action -eq 'build') {
    Push-Location $upstreamRoot
    try {
        & $goExecutable run ./cmd/compose-build -config ../../adapter/build.yaml -work ../../.test-run/composition
        if ($LASTEXITCODE -ne 0) { throw 'PC composition build failed.' }
    } finally { Pop-Location }
}
if ($Action -eq 'smoke') {
    & python (Join-Path $adapterRoot 'tests/pc_smoke.py')
    if ($LASTEXITCODE -ne 0) { throw 'PC simulator smoke test failed.' }
}
