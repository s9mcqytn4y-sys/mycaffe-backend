<#
.SYNOPSIS
  MyCaffe backend developer commands for Windows (owner decision I4: Go native + PostgreSQL in Docker).

.EXAMPLE
  ./scripts/dev.ps1 setup      # first time: .env, database, migrations, seed
  ./scripts/dev.ps1 run        # API on http://localhost:8080
  ./scripts/dev.ps1 login      # smoke-test staff login with the dev PIN
  ./scripts/dev.ps1 test-int   # PostgreSQL 18 integration tests in a throwaway container
  ./scripts/dev.ps1 demo-up    # public demo through a Cloudflare Quick Tunnel
#>
param(
    [Parameter(Position = 0)]
    [ValidateSet('help', 'doctor', 'setup', 'db', 'migrate', 'seed', 'run', 'login', 'test', 'test-int', 'check',
        'down', 'reset-db', 'demo-up', 'demo-url', 'demo-logs', 'demo-down')]
    [string]$Command = 'help'
)

$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

$DevCompose = 'deploy/docker/docker-compose.yml'
$DemoCompose = 'deploy/docker/docker-compose.demo.yml'
$TestContainer = 'mycaffe-pg-integration'
$TestPort = 55432

function Write-Step([string]$Message) { Write-Host "==> $Message" -ForegroundColor Cyan }

function Invoke-Native {
    param([string]$File, [string[]]$Arguments)
    & $File @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$File $($Arguments -join ' ') failed (exit $LASTEXITCODE)" }
}

function Invoke-DevCompose([string[]]$Arguments) {
    Invoke-Native docker (@('compose', '-f', $DevCompose, '--env-file', '.env') + $Arguments)
}

function Assert-EnvFile {
    if (-not (Test-Path .env)) {
        Copy-Item .env.example .env
        Write-Host '.env dibuat dari .env.example (nilai lokal saja; jangan dipakai di produksi).' -ForegroundColor Yellow
    }
}

# Force dev commands onto the local Docker PostgreSQL. DATABASE_URL in .env may point to a
# remote database; godotenv never overrides variables that are already set, so these win.
function Set-LocalDatabaseEnv {
    $vars = @{}
    Get-Content .env | Where-Object { $_ -match '^\s*([A-Z0-9_]+)\s*=\s*(.*)$' } | ForEach-Object {
        $vars[$Matches[1]] = $Matches[2].Trim().Trim('"').Trim("'")
    }
    $get = { param($key, $default) if ($vars[$key]) { $vars[$key] } else { $default } }
    $db = & $get 'DB_NAME' 'mycaffe_dev'
    $migUser = & $get 'DB_MIGRATOR_USER' 'mycaffe_migrator'
    $migPass = [uri]::EscapeDataString((& $get 'DB_MIGRATOR_PASSWORD' 'mycaffe_local_migrator_password'))
    $appUser = & $get 'DB_APP_USER' 'mycaffe_app'
    $appPass = & $get 'DB_APP_PASSWORD' 'mycaffe_local_app_password'
    $port = & $get 'DB_PORT' '5432'
    $env:DB_APP_USER = $appUser
    $env:DB_APP_PASSWORD = $appPass
    $env:DATABASE_MIGRATION_URL = "postgres://${migUser}:${migPass}@127.0.0.1:${port}/${db}?sslmode=disable"
    $env:DATABASE_URL = "postgres://${appUser}:$([uri]::EscapeDataString($appPass))@127.0.0.1:${port}/${db}?sslmode=disable"
    if (-not $env:APP_ENV) { $env:APP_ENV = 'development' }
    if ($vars['DATABASE_URL'] -and $vars['DATABASE_URL'] -notmatch '@(localhost|127\.0\.0\.1)[:/]') {
        Write-Host 'Catatan: DATABASE_URL di .env menunjuk ke host remote; dev.ps1 mengabaikannya dan memakai Postgres lokal.' -ForegroundColor Yellow
    }
}

function Wait-Healthy([string]$Container, [int]$Seconds = 60) {
    for ($i = 0; $i -lt $Seconds; $i++) {
        $status = docker inspect -f '{{.State.Health.Status}}' $Container 2>$null
        if ($status -eq 'healthy') { return }
        Start-Sleep -Seconds 1
    }
    throw "$Container tidak healthy dalam $Seconds detik"
}

function Get-DemoUrl {
    $logs = docker compose -f $DemoCompose logs tunnel 2>&1 | Out-String
    $found = [regex]::Matches($logs, 'https://[a-z0-9-]+\.trycloudflare\.com')
    if ($found.Count -eq 0) { return $null }
    return $found[$found.Count - 1].Value
}

switch ($Command) {
    'help' {
        Get-Help $PSCommandPath -Detailed | Out-Host
    }

    'doctor' {
        Write-Step 'Toolchain'
        go version
        docker version --format 'Docker {{.Server.Version}}'
        Write-Step 'Windows Firewall'
        Get-NetFirewallProfile | Format-Table Name, Enabled -AutoSize | Out-Host
        $category = (Get-NetConnectionProfile | Select-Object -First 1).NetworkCategory
        $public = Get-NetFirewallProfile -Name Public
        if (-not $public.Enabled) {
            Write-Host "PERINGATAN: profil Public mati (jaringan aktif: $category). Aktifkan sebagai Administrator:" -ForegroundColor Red
            Write-Host '  Set-NetFirewallProfile -Profile Public -Enabled True' -ForegroundColor Red
        }
        Write-Step 'Port MyCaffe yang terbuka (harus 127.0.0.1, bukan 0.0.0.0)'
        Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue |
            Where-Object { $_.LocalPort -in 80, 3000, 5432, 8080, 8088, $TestPort } |
            Sort-Object LocalPort | Format-Table LocalAddress, LocalPort -AutoSize | Out-Host
    }

    'setup' {
        Assert-EnvFile
        & $PSCommandPath db
        & $PSCommandPath migrate
        & $PSCommandPath seed
        Write-Host 'Siap. Jalankan: ./scripts/dev.ps1 run' -ForegroundColor Green
    }

    'db' {
        Assert-EnvFile
        Write-Step 'PostgreSQL 18 (127.0.0.1:5432)'
        Invoke-DevCompose @('up', '-d', 'postgres')
        Wait-Healthy 'mycaffe-postgres-dev'
    }

    'migrate' { Assert-EnvFile; Set-LocalDatabaseEnv; Invoke-Native go @('run', './cmd/migrate', '-action=up') }

    'seed' { Assert-EnvFile; Set-LocalDatabaseEnv; Invoke-Native go @('run', './cmd/migrate', '-action=seed') }

    'run' { Assert-EnvFile; Set-LocalDatabaseEnv; Invoke-Native go @('run', './cmd/api') }

    'login' {
        Write-Step 'POST /api/v1/auth/staff/login (central-cafe / cashier.central@mycaffe.local / PIN dev 482913)'
        $body = @{ store_slug = 'central-cafe'; email = 'cashier.central@mycaffe.local'; pin = '482913' } | ConvertTo-Json
        $resp = Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/api/v1/auth/staff/login' -ContentType 'application/json' -Body $body
        Write-Host "Masuk sebagai $($resp.staff.full_name) ($($resp.staff.role)) di $($resp.staff.store.name); access token sampai $($resp.access_token_expires_at), sesi sampai $($resp.session_expires_at)."
        $me = Invoke-RestMethod -Uri 'http://localhost:8080/api/v1/auth/staff/me' -Headers @{ Authorization = "Bearer $($resp.access_token)" }
        Write-Host "GET /me -> $($me.staff.full_name) / $($me.staff.role) / toko $($me.staff.store.slug)" -ForegroundColor Green
    }

    'test' { Invoke-Native go @('test', '-race', './...') }

    'test-int' {
        Write-Step "PostgreSQL 18 sekali pakai di 127.0.0.1:$TestPort"
        $existing = docker ps -a --format '{{.Names}}'
        if ($existing -contains $TestContainer) {
            docker rm -f $TestContainer | Out-Null
        }
        $initScript = (Resolve-Path 'deploy/postgres/00-init-roles.sh').Path
        Invoke-Native docker @('run', '-d', '--name', $TestContainer,
            '-e', 'POSTGRES_PASSWORD=it_super', '-e', 'POSTGRES_DB=mycaffe_test',
            '-e', 'DB_MIGRATOR_USER=mycaffe_migrator', '-e', 'DB_MIGRATOR_PASSWORD=it_migrator',
            '-e', 'DB_APP_USER=mycaffe_app', '-e', 'DB_APP_PASSWORD=it_app',
            '--health-cmd', 'pg_isready -U mycaffe_migrator -d mycaffe_test', '--health-interval', '1s',
            '-p', "127.0.0.1:${TestPort}:5432", '-v', "${initScript}:/docker-entrypoint-initdb.d/00-init-roles.sh:ro",
            'postgres:18-alpine')
        try {
            Wait-Healthy $TestContainer
            $migrator = "postgres://mycaffe_migrator:it_migrator@127.0.0.1:$TestPort/mycaffe_test?sslmode=disable"
            $env:APP_ENV = 'test'
            $env:DATABASE_URL = $migrator
            $env:DATABASE_MIGRATION_URL = $migrator
            $env:DB_APP_USER = 'mycaffe_app'
            $env:DB_APP_PASSWORD = 'it_app'
            Invoke-Native go @('run', './cmd/migrate', '-action=up')
            Invoke-Native go @('run', './cmd/migrate', '-action=seed')
            $env:TEST_APP_DATABASE_URL = "postgres://mycaffe_app:it_app@127.0.0.1:$TestPort/mycaffe_test?sslmode=disable"
            $env:TEST_MIGRATOR_DATABASE_URL = $migrator
            Invoke-Native go @('test', '-race', '-count=1', './...')
        }
        finally {
            docker rm -f $TestContainer 2>$null | Out-Null
            'APP_ENV', 'DATABASE_URL', 'DATABASE_MIGRATION_URL', 'DB_APP_USER', 'DB_APP_PASSWORD',
            'TEST_APP_DATABASE_URL', 'TEST_MIGRATOR_DATABASE_URL' | ForEach-Object { Remove-Item "Env:$_" -ErrorAction SilentlyContinue }
        }
    }

    'check' {
        Write-Step 'gofmt / vet / tidy / govulncheck / race tests'
        $unformatted = gofmt -l cmd internal
        if ($unformatted) { throw "gofmt diperlukan untuk: $unformatted" }
        Invoke-Native go @('vet', './...')
        Invoke-Native go @('mod', 'tidy', '-diff')
        if (Get-Command govulncheck -ErrorAction SilentlyContinue) { Invoke-Native govulncheck @('./...') }
        else { Write-Host 'govulncheck tidak terpasang: go install golang.org/x/vuln/cmd/govulncheck@latest' -ForegroundColor Yellow }
        Invoke-Native go @('test', '-race', './...')
    }

    'down' { Invoke-DevCompose @('down') }

    'reset-db' {
        Write-Host 'Menghapus volume database DEV lokal (data dev/seed saja).' -ForegroundColor Yellow
        Invoke-DevCompose @('down', '-v')
        & $PSCommandPath setup
    }

    'demo-up' {
        Write-Step 'Stack demo (PostgreSQL, API staging, Nginx, Cloudflare Quick Tunnel)'
        if (-not (Test-Path .env.demo)) { throw 'Buat .env.demo dari .env.demo.example (JWT_SECRET_KEY, DEMO_OWNER_PIN, DEMO_STAFF_PIN wajib).' }
        $envArgs = @('--env-file', '.env.demo')
        Invoke-Native docker (@('compose', '-f', $DemoCompose) + $envArgs + @('up', '-d', '--build'))
        for ($i = 0; $i -lt 30 -and -not (Get-DemoUrl); $i++) { Start-Sleep -Seconds 2 }
        & $PSCommandPath demo-url
    }

    'demo-url' {
        $url = Get-DemoUrl
        if (-not $url) { throw 'URL tunnel belum muncul; cek: ./scripts/dev.ps1 demo-logs' }
        Write-Host "Demo publik : $url" -ForegroundColor Green
        Write-Host "Health      : $url/health/live"
        Write-Host "Webhook     : $url/api/v1/payments/midtrans/webhook (set di dashboard Midtrans sandbox)"
    }

    'demo-logs' { docker compose -f $DemoCompose logs --tail 100 -f }

    'demo-down' { Invoke-Native docker @('compose', '-f', $DemoCompose, 'down') }
}
