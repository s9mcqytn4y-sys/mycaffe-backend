#Requires -Version 5.1
<#
.SYNOPSIS
    MyCaffe Workstation & Network Doctor (Windows 11 + Cloudflare WARP + WSL2 + Docker + Named Tunnel)
.DESCRIPTION
    Memeriksa kesehatan jaringan laptop Windows 11, resolusi DNS Cloudflare, konektivitas R2 CDN & Demo Tunnel,
    port lokal PostgreSQL/API, serta menyediakan opsi menjalankan Cloudflare Named Tunnel `mycaffe-demo`.
.PARAMETER StartTunnel
    Jika disertakan, langsung menyalakan tunnel `mycaffe-demo` (`https://demo.abbdullaziz.online` -> `http://localhost:8080`).
#>
param(
    [switch]$StartTunnel
)

$ErrorActionPreference = 'Continue'

Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "  MYCAFFE WORKSTATION & NETWORK DOCTOR (WINDOWS 11)             " -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan

# 1. Active Interfaces & DNS Servers
Write-Host "`n[1/5] Active Network Interfaces & DNS Configuration:" -ForegroundColor Yellow
Get-NetIPConfiguration | Where-Object { $_.NetAdapter.Status -eq 'Up' } | ForEach-Object {
    $iface = $_.InterfaceAlias
    $ipv4  = ($_.IPv4Address.IPAddress -join ', ')
    $dns   = ($_.DnsServer.ServerAddresses -join ', ')
    Write-Host ("  - {0,-35} | IPv4: {1,-15} | DNS: {2}" -f $iface, $ipv4, $dns)
}

# 2. DNS Resolution & Latency Check
Write-Host "`n[2/5] Cloudflare DNS & Domain Resolution (`abbdullaziz.online`):" -ForegroundColor Yellow
foreach ($hostName in @("abbdullaziz.online", "demo.abbdullaziz.online", "cdn.abbdullaziz.online", "api.sandbox.midtrans.com")) {
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        $resolved = Resolve-DnsName -Name $hostName -ErrorAction Stop | Select-Object -First 2
        $sw.Stop()
        $ips = ($resolved | ForEach-Object { if ($_.IPAddress) { $_.IPAddress } else { $_.NameHost } }) -join ', '
        Write-Host ("  [OK] {0,-28} -> {1} ({2} ms)" -f $hostName, $ips, $sw.ElapsedMilliseconds) -ForegroundColor Green
    } catch {
        $sw.Stop()
        Write-Host ("  [!!] {0,-28} -> FAILED ({1})" -f $hostName, $_.Exception.Message) -ForegroundColor Red
    }
}

# 3. Local Dev Ports Check (PostgreSQL 5432, Go API 8080)
Write-Host "`n[3/5] Local Development Ports (localhost):" -ForegroundColor Yellow
foreach ($portInfo in @(
    @{ Port = 5432; Service = "PostgreSQL 18" },
    @{ Port = 8080; Service = "MyCaffe Go API" }
)) {
    $tcp = Get-NetTCPConnection -LocalPort $portInfo.Port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($tcp) {
        Write-Host ("  [LISTENING] Port {0} ({1}) - PID {2}" -f $portInfo.Port, $portInfo.Service, $tcp.OwningProcess) -ForegroundColor Green
    } else {
        Write-Host ("  [IDLE]      Port {0} ({1}) is currently free" -f $portInfo.Port, $portInfo.Service) -ForegroundColor DarkGray
    }
}

# 4. Toolchain Verification
Write-Host "`n[4/5] Core CLI Toolchain Status:" -ForegroundColor Yellow
$tools = @(
    @{ Name = "claude";      Cmd = { claude --version } },
    @{ Name = "agy";         Cmd = { & "C:\Users\Acer\AppData\Local\agy\bin\agy.exe" --version } },
    @{ Name = "python";      Cmd = { & "C:\Program Files\Python312\python.exe" --version } },
    @{ Name = "uv";          Cmd = { & "C:\Users\Acer\AppData\Roaming\Python\Python312\Scripts\uv.exe" --version } },
    @{ Name = "go";          Cmd = { go version } },
    @{ Name = "node";        Cmd = { node --version } },
    @{ Name = "docker";      Cmd = { docker --version } },
    @{ Name = "cloudflared"; Cmd = { cloudflared --version } },
    @{ Name = "wrangler";    Cmd = { wrangler --version } },
    @{ Name = "blender";     Cmd = { & "C:\Program Files\Blender Foundation\Blender 5.2\blender.exe" --version | Select-Object -First 1 } }
)
foreach ($t in $tools) {
    try {
        $out = (& $t.Cmd 2>&1 | Select-Object -First 1).ToString().Trim()
        Write-Host ("  [OK] {0,-14} : {1}" -f $t.Name, $out) -ForegroundColor Green
    } catch {
        Write-Host ("  [!!] {0,-14} : NOT FOUND" -f $t.Name) -ForegroundColor Red
    }
}

# 5. Optional Cloudflare Tunnel Startup
Write-Host "`n[5/5] Cloudflare Named Tunnel (`mycaffe-demo`):" -ForegroundColor Yellow
if ($StartTunnel) {
    Write-Host "  Starting Cloudflare Tunnel mycaffe-demo -> http://localhost:8080 ..." -ForegroundColor Cyan
    cloudflared tunnel --config "$PSScriptRoot\..\deploy\cloudflare\config.demo.yml" run mycaffe-demo
} else {
    Write-Host "  Ready. Run '.\scripts\dev-network-doctor.ps1 -StartTunnel' to expose http://localhost:8080 at https://demo.abbdullaziz.online" -ForegroundColor Cyan
}

