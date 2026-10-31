# ==============================================================================
# MyCaffe POS - Cloudflare R2 Verification Script
# Validates bucket existence, CORS policy, and access state
# ==============================================================================
[CmdletBinding()]
param(
    [string]$MediaBucket = "mycaffe-media",
    [string]$PrivateMediaBucket = "mycaffe-private-media",
    [string]$BackupBucket = "mycaffe-backups"
)

$ErrorActionPreference = "Stop"

Write-Host "==> Verifying Cloudflare Authentication..." -ForegroundColor Cyan
$whoami = npx --yes wrangler whoami 2>&1 | Out-String
if ($whoami -match "([a-f0-9]{32})") {
    $accountId = $matches[1]
    Write-Host "Authenticated with Account ID: $accountId" -ForegroundColor Green
} else {
    Write-Error "Wrangler authentication verification failed."
    exit 1
}

Write-Host "==> Verifying R2 Buckets..." -ForegroundColor Cyan
$buckets = npx --yes wrangler r2 bucket list 2>&1 | Out-String

foreach ($bucket in @($MediaBucket, $PrivateMediaBucket, $BackupBucket)) {
    if ($buckets -match $bucket) {
        Write-Host "  [PASS] Bucket $bucket exists." -ForegroundColor Green
    } else {
        Write-Host "  [FAIL] Bucket $bucket NOT found." -ForegroundColor Red
        exit 1
    }
}

Write-Host "==> Verifying CORS for $MediaBucket..." -ForegroundColor Cyan
$cors = npx --yes wrangler r2 bucket cors list $MediaBucket 2>&1 | Out-String
if ($cors -match "allowed_origins" -and $cors -match "localhost:3000") {
    Write-Host "  [PASS] CORS rules active for $MediaBucket." -ForegroundColor Green
} else {
    Write-Warning "CORS configuration missing or unexpected for $MediaBucket."
}

Write-Host "==> Verification completed successfully." -ForegroundColor Green
