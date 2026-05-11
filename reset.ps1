<#
.SYNOPSIS
    DAOF-CPA 项目重置脚本：清理所有缓存、构建产物、本地数据库，重新编译前端。

.DESCRIPTION
    模拟"全新部署"场景的本地完整重置。流程：
      1. 检查/Kill 正在运行的 go / vite / 已构建二进制进程
      2. 清理 Go 构建/测试缓存（go clean -cache -testcache）
      3. 删除本地 data/*（SQLite DB + AES 主密钥）
      4. 删除前端产物 ui/dist + vite 缓存 ui/node_modules/.vite
      5. 删除散落的 *.exe / *.log
      6. 重新编译前端（npm run build）
      7. 校验 Go 全包编译

.PARAMETER NoConfirm
    无交互模式：跳过所有 Read-Host 确认，直接执行（CI/批处理用）。

.PARAMETER KeepData
    保留 data/ 目录（DB + 密钥）。开发中频繁迭代时用，不丢用户数据。

.PARAMETER CleanNodeModules
    一并删除 ui/node_modules（依赖大版本变动后用，重装较慢约 30s）。

.PARAMETER SkipFrontend
    跳过前端 npm run build（只清理）。

.PARAMETER SkipGoBuild
    跳过 go build ./... 校验（只清理 + 前端）。

.EXAMPLE
    .\reset.ps1
    交互式完整重置。

.EXAMPLE
    .\reset.ps1 -NoConfirm
    无交互完整重置（CI 用）。

.EXAMPLE
    .\reset.ps1 -KeepData
    保留 DB，只清缓存与前端产物（开发迭代用）。

.EXAMPLE
    .\reset.ps1 -KeepData -SkipFrontend -SkipGoBuild
    只清 Go 缓存（最轻）。
#>

[CmdletBinding()]
param(
    [switch]$NoConfirm,
    [switch]$KeepData,
    [switch]$CleanNodeModules,
    [switch]$SkipFrontend,
    [switch]$SkipGoBuild
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

# ─── UI helpers ─────────────────────────────────────
function Write-Step { Write-Host "▶ $args" -ForegroundColor Cyan }
function Write-Ok   { Write-Host "✓ $args" -ForegroundColor Green }
function Write-Warn { Write-Host "⚠ $args" -ForegroundColor Yellow }
function Write-Err  { Write-Host "✗ $args" -ForegroundColor Red }

function Confirm-Or-Exit {
    param([string]$Prompt, [string]$AbortMsg = '已取消')
    if ($NoConfirm) { return $true }
    $ans = Read-Host "$Prompt (y/N)"
    if ($ans -eq 'y' -or $ans -eq 'Y') { return $true }
    Write-Warn $AbortMsg
    return $false
}

$startTime = Get-Date

Write-Host ""
Write-Host "🧹 DAOF-CPA Reset Script" -ForegroundColor Magenta
Write-Host "   $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')   PWD: $PWD"
Write-Host ""

# ─── 1. 检查正在运行的进程 ──────────────────────────
Write-Step "检查正在运行的相关进程..."
$running = @()
$running += Get-Process -Name 'go','main','daof-ai-hub','engine' -ErrorAction SilentlyContinue
# 检测 vite/node 仅匹配 daof-ai-hub 路径下的，避免误杀其它项目的 node
$running += Get-Process -Name 'node' -ErrorAction SilentlyContinue | Where-Object {
    try { $_.Path -like "*daof-ai-hub*" } catch { $false }
}
if ($running) {
    Write-Warn "检测到 $($running.Count) 个相关进程仍在运行："
    $running | ForEach-Object {
        Write-Host ("    PID {0,-6} {1,-15} {2}" -f $_.Id, $_.ProcessName, $_.Path) -ForegroundColor DarkGray
    }
    if (Confirm-Or-Exit "是否 Kill 这些进程？" "存在运行中进程，请手动停止后重试") {
        $running | Stop-Process -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 500  # 让 Windows 释放文件锁
        Write-Ok "已 Kill 相关进程"
    } else {
        exit 1
    }
} else {
    Write-Ok "无运行中进程"
}

# ─── 2. 清理 data/ ──────────────────────────────────
if ($KeepData) {
    Write-Ok "保留 data/（-KeepData）"
} else {
    Write-Step "清理 data/ （SQLite DB + AES 主密钥）..."
    if (Test-Path "data") {
        $items = @(Get-ChildItem -Path "data" -Force -ErrorAction SilentlyContinue)
        if ($items.Count -gt 0) {
            Write-Warn "data/ 内将被删除的文件："
            $items | ForEach-Object {
                $size = if ($_.PSIsContainer) { "(dir)" } else { "$([math]::Round($_.Length/1KB,2)) KB" }
                Write-Host "    $($_.Name)  $size" -ForegroundColor DarkGray
            }
            if (Confirm-Or-Exit "继续删除？此操作清空所有用户/订阅/账单数据" "保留 data/ 不动") {
                Get-ChildItem -Path "data" -Force | Remove-Item -Recurse -Force
                Write-Ok "data/ 已清空"
            }
        } else {
            Write-Ok "data/ 本身已为空"
        }
    } else {
        New-Item -ItemType Directory -Path "data" -Force | Out-Null
        Write-Ok "data/ 已创建（之前不存在）"
    }
}

# ─── 3. 清理 Go 缓存 ────────────────────────────────
Write-Step "清理 Go 构建 / 测试缓存..."
& go clean -cache 2>&1 | Out-Null
& go clean -testcache 2>&1 | Out-Null
Write-Ok "Go cache cleaned"

# ─── 4. 清理散落的 *.exe / *.log ────────────────────
Write-Step "清理散落的 *.exe / *.log..."
$strays = @()
$strays += Get-ChildItem -Path . -Filter "*.exe" -File -ErrorAction SilentlyContinue
$strays += Get-ChildItem -Path . -Filter "*.log" -File -ErrorAction SilentlyContinue
if ($strays.Count -gt 0) {
    $strays | ForEach-Object { Write-Host "    rm $($_.Name)" -ForegroundColor DarkGray }
    $strays | Remove-Item -Force -ErrorAction SilentlyContinue
    Write-Ok "已删除 $($strays.Count) 个散落文件"
} else {
    Write-Ok "无散落文件"
}

# ─── 5. 清理前端产物 ───────────────────────────────
Write-Step "清理 ui/dist + vite cache..."
if (Test-Path "ui/dist") {
    Remove-Item "ui/dist" -Recurse -Force
    Write-Ok "ui/dist 已删"
} else {
    Write-Ok "ui/dist 不存在"
}
if (Test-Path "ui/node_modules/.vite") {
    Remove-Item "ui/node_modules/.vite" -Recurse -Force
    Write-Ok "ui/node_modules/.vite 已删"
}
if ($CleanNodeModules) {
    if (Test-Path "ui/node_modules") {
        Write-Step "删除 ui/node_modules（-CleanNodeModules）..."
        Remove-Item "ui/node_modules" -Recurse -Force
        Write-Ok "ui/node_modules 已删（重新 build 时会自动 npm install）"
    }
}

# ─── 6. 重建前端 ───────────────────────────────────
if ($SkipFrontend) {
    Write-Warn "跳过前端 build（-SkipFrontend）"
} else {
    Push-Location ui
    try {
        if (-not (Test-Path "node_modules")) {
            Write-Step "未检测到 node_modules，先 npm install..."
            & npm install
            if ($LASTEXITCODE -ne 0) { throw "npm install 失败 (exit=$LASTEXITCODE)" }
            Write-Ok "npm install 完成"
        }
        Write-Step "编译前端 (npm run build)..."
        & npm run build
        if ($LASTEXITCODE -ne 0) { throw "npm run build 失败 (exit=$LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
    if (Test-Path "ui/dist") {
        $distSize = (Get-ChildItem "ui/dist" -Recurse -File | Measure-Object Length -Sum).Sum
        Write-Ok ("前端 build 完成 (dist={0:N1} MB)" -f ($distSize / 1MB))
    }
}

# ─── 7. 校验 Go 编译 ───────────────────────────────
if ($SkipGoBuild) {
    Write-Warn "跳过 Go build 校验（-SkipGoBuild）"
} else {
    Write-Step "校验 Go 全包编译 (go build ./...)..."
    # CGO 需要的环境（与 start.ps1 对齐；本机若无 TDM-GCC 会自动 fallback 到 PATH 里的 gcc）
    if (-not $env:CC -and (Test-Path "C:\TDM-GCC-64\bin\gcc.exe")) {
        $env:CC = "C:\TDM-GCC-64\bin\gcc.exe"
    }
    if (-not $env:CGO_ENABLED) {
        $env:CGO_ENABLED = "1"
    }
    & go build ./...
    if ($LASTEXITCODE -ne 0) {
        Write-Err "Go build 失败"
        exit 1
    }
    Write-Ok "Go build 干净通过"
}

# ─── 8. 总结 ───────────────────────────────────────
$elapsed = (Get-Date) - $startTime
Write-Host ""
Write-Host "═══════════════════════════════════════════" -ForegroundColor DarkGray
Write-Host ("  ✨ 重置完成（耗时 {0:N1}s）" -f $elapsed.TotalSeconds) -ForegroundColor Green
Write-Host "═══════════════════════════════════════════" -ForegroundColor DarkGray
Write-Host ""
Write-Host "下一步：" -ForegroundColor Cyan
Write-Host "  .\start.ps1     # 启动后端（go run main.go）"
Write-Host ""
if (-not $KeepData) {
    Write-Host "首次启动会做：" -ForegroundColor DarkGray
    Write-Host "  • 生成新 SQLite DB (data/daofa-hub.db)" -ForegroundColor DarkGray
    Write-Host "  • 生成新 AES 主密钥 (data/daof.key)" -ForegroundColor DarkGray
    Write-Host "  • AutoMigrate + Seed 默认 SysConfig" -ForegroundColor DarkGray
    Write-Host "  • 第一个注册用户自动成为 admin" -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "登录后需手填：" -ForegroundColor DarkGray
    Write-Host "  • 易付通 V2 商户私钥 + 平台公钥（admin → 支付通道）" -ForegroundColor DarkGray
    Write-Host "  • GitHub OAuth Client ID/Secret（admin → 系统）" -ForegroundColor DarkGray
    Write-Host ""
}
