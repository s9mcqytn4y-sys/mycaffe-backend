# MyCaffe Backend Docs

Konvensi: folder dan file memakai **PascalCase** (`Architecture/Roadmap.md`). `README.md` tetap memakai
nama konvensi GitHub. Dokumen yang hanya bernilai historis ada di `Archive/` dan tidak diperbarui lagi.

| Folder | Isi | Mulai dari |
|--------|-----|-----------|
| `Product/` | Merek, kebutuhan per peran, modul, assessment owner, PRD v0.9 lantai kafe, rencana analytics | [PrdV09CafeFloor.md](Product/PrdV09CafeFloor.md) |
| `Architecture/` | ADR, keputusan owner, roadmap, kontrak API/DB, risiko keamanan | [Roadmap.md](Architecture/Roadmap.md), [DecisionRegister.md](Architecture/DecisionRegister.md) |
| `Api/` | Kontrak OpenAPI (sumber kebenaran endpoint) | [OpenApi.yaml](Api/OpenApi.yaml), [FrontendIntegrationContract.md](Architecture/FrontendIntegrationContract.md) |
| `Engineering/` | Backlog audit, workflow Git dan rilis, gaya Go, audit dependensi | [AuditBacklog.md](Engineering/AuditBacklog.md) |
| `Setup/` | Dev Windows, demo, deploy, runbook operasi, backup, email, monitoring, R2, tooling AI | [DemoReadinessChecklist.md](Setup/DemoReadinessChecklist.md) |
| `Archive/` | Rencana iterasi 4–5, audit lama, evaluasi 2026-10 | Hanya referensi |
