$ErrorActionPreference = 'Stop'
Push-Location (Join-Path $PSScriptRoot '..')
$previousBin = $env:GOBIN
try {
    $env:GOBIN = Join-Path (Get-Location) '.local\bin'
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
    if ($LASTEXITCODE -ne 0) { throw 'protoc-gen-go installation failed' }
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
    if ($LASTEXITCODE -ne 0) { throw 'protoc-gen-go-grpc installation failed' }
    $compiler = Join-Path (Get-Location) '.local\protoc\bin\protoc.exe'
    if (-not (Test-Path -LiteralPath $compiler)) { $compiler = (Get-Command protoc -ErrorAction Stop).Source }
    & $compiler '--plugin=protoc-gen-go=.local/bin/protoc-gen-go.exe' '--plugin=protoc-gen-go-grpc=.local/bin/protoc-gen-go-grpc.exe' --go_out=. --go_opt=module=github.com/tsostanov/SeatFlow --go-grpc_out=. --go-grpc_opt=module=github.com/tsostanov/SeatFlow api/booking/v1/platform.proto
    if ($LASTEXITCODE -ne 0) { throw 'protobuf generation failed' }
} finally {
    $env:GOBIN = $previousBin
    Pop-Location
}
