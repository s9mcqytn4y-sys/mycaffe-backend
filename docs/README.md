# MyCaffe Backend Docs

Konvensi: folder dan file memakai **PascalCase** (`Architecture/Roadmap.md`). `README.md` tetap memakai
nama konvensi GitHub. Dokumen yang hanya bernilai historis ada di `Archive/` dan tidak diperbarui lagi.

| Folder | Isi | Mulai dari |
|--------|-----|-----------|
| `Product/` | Merek, kebutuhan per peran, modul, assessment owner, draf PRD v2 | [OwnerOperationalAssessment.md](Product/OwnerOperationalAssessment.md) |
| `Architecture/` | ADR, keputusan owner, roadmap, kontrak API/DB, risiko keamanan | [Roadmap.md](Architecture/Roadmap.md), [DecisionRegister.md](Architecture/DecisionRegister.md) |
| `Api/` | Kontrak OpenAPI (sumber kebenaran endpoint) | [OpenApi.yaml](Api/OpenApi.yaml), [FrontendIntegrationContract.md](Architecture/FrontendIntegrationContract.md) |
| `Engineering/` | Backlog audit, workflow Git dan rilis, gaya Go, audit dependensi | [AuditBacklog.md](Engineering/AuditBacklog.md) |
| `Setup/` | Dev Windows, demo, deploy, backup, email, monitoring, R2, tooling AI | [DemoReadinessChecklist.md](Setup/DemoReadinessChecklist.md) |
| `Archive/` | Rencana iterasi 4–5, audit lama, evaluasi 2026-10 | Hanya referensi |
