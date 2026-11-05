# MyCaffe POS - Cloudflare Free Plan Setup Script (abbdullaziz.online)
# Configures:
#   1. Named Tunnel DNS CNAME: demo.abbdullaziz.online -> mycaffe-demo (85b97ba7-79d7-48d7-b045-b62645435708)
#   2. R2 Media Custom Domain: cdn.abbdullaziz.online -> mycaffe-media (+ CORS policy)
#   3. Email Routing Rules: noreply@, owner@, support@, and catch-all *@abbdullaziz.online -> Owner Gmail

$ErrorActionPreference = "Stop"
$ZoneId   = "c4d1c304cb402f372016fb554df434c2"
$TunnelId = "85b97ba7-79d7-48d7-b045-b62645435708"

Write-Host "=== [1/3] Cloudflare Named Tunnel DNS Route (demo.abbdullaziz.online) ===" -ForegroundColor Cyan
$certPath = Join-Path $HOME ".cloudflared\cert.pem"
if (-not (Test-Path $certPath)) {
    Write-Host "Running 'cloudflared tunnel login' - please select 'abbdullaziz.online' in your browser..." -ForegroundColor Yellow
    cloudflared tunnel login
}
cloudflared tunnel route dns $TunnelId demo.abbdullaziz.online

Write-Host "=== [2/3] R2 Bucket CORS & Custom Domain (cdn.abbdullaziz.online) ===" -ForegroundColor Cyan
npx wrangler r2 bucket cors set mycaffe-media --file config/cloudflare/r2-media-cors.json --force
npx wrangler r2 bucket domain add mycaffe-media --domain cdn.abbdullaziz.online --zone-id $ZoneId --force

Write-Host "=== [3/3] Cloudflare Email Routing Rules (*@abbdullaziz.online) ===" -ForegroundColor Cyan
$wranglerCfg = Get-Content "$env:APPDATA\xdg.config\.wrangler\config\default.toml" -Raw
$token = [regex]::Match($wranglerCfg, 'oauth_token\s*=\s*"([^"]+)"').Groups[1].Value
$headers = @{ Authorization = "Bearer $token"; "Content-Type" = "application/json" }

$envContent = Get-Content ".env" -Raw
$destEmail = [regex]::Match($envContent, 'SMTP_USERNAME=([^\r\n]+)').Groups[1].Value.Trim()

foreach ($local in @("noreply", "owner", "support")) {
    $body = @{
        name     = "Forward $local to Owner Gmail"
        enabled  = $true
        matchers = @(@{ type = "literal"; field = "to"; value = "$local@abbdullaziz.online" })
        actions  = @(@{ type = "forward"; value = @($destEmail) })
    } | ConvertTo-Json -Depth 6
    try {
        $res = Invoke-RestMethod -Uri "https://api.cloudflare.com/client/v4/zones/$ZoneId/email/routing/rules" -Method Post -Headers $headers -Body $body
        Write-Host "  [OK] $local@abbdullaziz.online -> forwarded (success=$($res.success))" -ForegroundColor Green
    } catch {
        Write-Host "  [WARN] $local@abbdullaziz.online: Verify destination email in Gmail first ($($_.Exception.Message))" -ForegroundColor Yellow
    }
}

