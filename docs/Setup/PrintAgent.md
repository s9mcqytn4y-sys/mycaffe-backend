# Print Agent (decision W13)

The print agent runs on a device in the cafe's LAN, claims the server's print jobs (PR 9.4) for each
printer it serves, sends the bytes to the printer's raw port (`IP:9100`) and acknowledges the result. The
server renders every ticket and label; the agent never builds content itself (until W18 offline printing).

## 1. Prepare the printers

1. Give each thermal printer a fixed IP in the router (DHCP reservation) and check raw printing on port 9100.
2. In the owner backoffice create one printer per device and station (`POST /api/v1/store/printers`). Copy the
   **agent token** shown once; rotate it (`POST /api/v1/store/printers/:id/rotate-token`) when it leaks or the
   agent is reinstalled.

## 2. Build

From the repository (`CGO_ENABLED=0`, no runtime dependencies):

```bash
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o print-agent.exe ./cmd/print-agent
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o print-agent ./cmd/print-agent
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o print-agent-arm64 ./cmd/print-agent
```

## 3. Configure

`print-agent.json` (keep it readable only by the account running the agent: the tokens are secrets):

```json
{
  "api_url": "https://demo.abbdullaziz.online",
  "poll_seconds": 3,
  "printers": [
    {"name": "Bar", "token": "<agent token of the bar printer>", "address": "192.168.1.50:9100"},
    {"name": "Dapur", "token": "<agent token of the kitchen printer>", "address": "192.168.1.51:9100"}
  ]
}
```

## 4. Run as a service

Windows (PowerShell as administrator; the binary detects the service manager):

```powershell
sc.exe create MyCaffePrintAgent binPath= "C:\MyCaffe\print-agent.exe -config C:\MyCaffe\print-agent.json" start= auto
sc.exe failure MyCaffePrintAgent reset= 86400 actions= restart/5000
sc.exe start MyCaffePrintAgent
```

Linux (STB, mini PC, Raspberry Pi), `/etc/systemd/system/mycaffe-print-agent.service`:

```ini
[Unit]
Description=MyCaffe print agent
After=network-online.target
Wants=network-online.target

[Service]
User=mycaffe-print
ExecStart=/opt/mycaffe/print-agent -config /etc/mycaffe/print-agent.json
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## 5. Behaviour

- Polls every `poll_seconds` per printer and drains the queue without waiting while jobs remain.
- A printer that cannot be reached is acknowledged `failed`; the server retries with a growing delay and
  marks the job dead after 5 attempts (`print_job.failed` on the POS channel).
- A crash between printing and acknowledging re-prints after the server's 60 s lease (at-least-once).
- A refused token (rotated, printer inactive) is logged and retried every minute.
