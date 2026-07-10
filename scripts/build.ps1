# LQBOT 构建脚本
# 用法: .\scripts\build.ps1 [windows|linux|all]

param(
    [Parameter(Position=0)]
    [ValidateSet("windows", "linux", "all")]
    [string]$Target = "all"
)

$GOPROXY = "https://goproxy.cn,direct"
$ROOT = Split-Path -Parent (Split-Path -Parent $PSCommandPath)

# Git version injection
$GIT_COMMIT = & { git rev-parse --short HEAD 2>$null }
if (-not $GIT_COMMIT) { $GIT_COMMIT = "unknown" }
$GIT_DATE = & { Get-Date -Format "yyyyMMdd" 2>$null }
if (-not $GIT_DATE) { $GIT_DATE = "unknown" }
$VERSION_PKG = "github.com/Luoyangan/LQBOT/internal/version"
$LDFLAGS = "-s -w -X '$VERSION_PKG.Commit=$GIT_COMMIT' -X '$VERSION_PKG.Date=$GIT_DATE'"

# Read default version from source
$VERSION = & { Select-String -Path "$ROOT\internal\version\version.go" -Pattern 'Version\s*=\s*"(.+)"' | ForEach-Object { $_.Matches.Groups[1].Value } }

function Build-Windows {
    Write-Host "[build] Windows amd64  $GIT_COMMIT ($GIT_DATE) ..." -ForegroundColor Green
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    go build -ldflags="$LDFLAGS" -o "$ROOT\LQBOT-v$VERSION-$GIT_COMMIT.exe" "$ROOT\cmd\bot\main.go"
    if ($LASTEXITCODE -eq 0) {
        Write-Host "[done] LQBOT-v$VERSION-$GIT_COMMIT.exe" -ForegroundColor Green
    }
}

function Build-Linux {
    Write-Host "[build] Linux amd64  $GIT_COMMIT ($GIT_DATE) ..." -ForegroundColor Green
    $env:GOOS = "linux"
    $env:GOARCH = "amd64"
    go build -ldflags="$LDFLAGS" -o "$ROOT\LQBOT-v$VERSION-$GIT_COMMIT-linux" "$ROOT\cmd\bot\main.go"
    if ($LASTEXITCODE -eq 0) {
        Write-Host "[done] LQBOT-v$VERSION-$GIT_COMMIT-linux" -ForegroundColor Green
    }
}

$env:GOPROXY = $GOPROXY

switch ($Target) {
    "windows" { Build-Windows }
    "linux"   { Build-Linux }
    "all"     { Build-Windows; Build-Linux }
}
