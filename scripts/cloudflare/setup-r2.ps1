# ==============================================================================
# MyCaffe POS - Cloudflare R2 Provisioning Script
# Idempotent provisioning of R2 buckets (media, private-media, backups) and CORS
# ==============================================================================
[CmdletBinding()]
param(
    [string]$MediaBucket = "mycaffe-media",
    [string]$PrivateMediaBucket = "mycaffe-private-media",
    [string]$BackupBucket = "mycaffe-backups",
    [string]$CorsFile = "config/cloudflare/r2-media-cors.json"
)

$ErrorActionPreference = "Stop"

Write-Host "==> Checking Cloudflare Wrangler CLI..." -ForegroundColor Cyan
try {
    $whoami = npx --yes wrangler whoami 2>&1 | Out-String
    if ($whoami -match "You are not authenticated") {
        Write-Error "Cloudflare Wrangler is not authenticated. Please run 'npx wrangler login' first."
        exit 1
    }
    Write-Host "Wrangler authenticated." -ForegroundColor Green
} catch {
    Write-Error "Failed to execute wrangler: $_"
    exit 1
}

Write-Host "==> Querying existing R2 buckets..." -ForegroundColor Cyan
$existingBuckets = npx --yes wrangler r2 bucket list 2>&1 | Out-String

function Ensure-Bucket([string]$bucketName, [bool]$isPublic) {
    if ($existingBuckets -match $bucketName) {
        Write-Host "Bucket '$bucketName' already exists. Skipping creation." -ForegroundColor Yellow
    } else {
        Write-Host "Creating bucket '$bucketName'..." -ForegroundColor Cyan
        npx --yes wrangler r2 bucket create $bucketName
        Write-Host "Bucket '$bucketName' created successfully." -ForegroundColor Green
    }
}

Ensure-Bucket $MediaBucket $true
Ensure-Bucket $PrivateMediaBucket $false
Ensure-Bucket $BackupBucket $false

Write-Host "==> Configuring CORS for $MediaBucket..." -ForegroundColor Cyan
if (Test-Path $CorsFile) {
    npx --yes wrangler r2 bucket cors set $MediaBucket --file $CorsFile
    Write-Host "CORS configured for $MediaBucket using $CorsFile." -ForegroundColor Green
} else {
    Write-Warning "CORS configuration file not found at $CorsFile"
}

Write-Host "==> Provisioning complete." -ForegroundColor Green
Write-Host "Next steps:" -ForegroundColor Cyan
Write-Host "1. Create S3 API credentials via Cloudflare Dashboard: R2 > Manage R2 API Tokens"
Write-Host "   - Token 1 (Media): Read & Write for '$MediaBucket' and '$PrivateMediaBucket'"
Write-Host "   - Token 2 (Backup): Read & Write for '$BackupBucket' only"
Write-Host "2. Populate local .env (never commit secrets to Git)."
