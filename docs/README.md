# MyCaffe Backend Docs

Konvensi: folder dan file memakai **PascalCase** (`Architecture/Roadmap.md`). `README.md` tetap memakai
nama konvensi GitHub. Dokumen yang hanya bernilai historis ada di `Archive/` dan tidak diperbarui lagi.

| Folder | Isi | Mulai dari |
|--------|-----|-----------|
| `Product/` | Merek, kebutuhan per peran, modul, analisis kompetitor (`InterActive`, `Majoo`), rencana rilis QR Table Order v1.0 | [ReleasePlanQrTableOrderV1.md](Product/ReleasePlanQrTableOrderV1.md), [CompetitorAnalysisInterActive.md](Product/CompetitorAnalysisInterActive.md) |
| `Architecture/` | ADR (ADR-001 s.d. ADR-009), keputusan owner, roadmap, kontrak API/DB, risiko keamanan | [Roadmap.md](Architecture/Roadmap.md), [Adr009MultiTenantSaasGovernanceAndByok.md](Architecture/Adr009MultiTenantSaasGovernanceAndByok.md) |
| `Api/` | Kontrak OpenAPI (sumber kebenaran endpoint) | [OpenApi.yaml](Api/OpenApi.yaml), [FrontendIntegrationContract.md](Architecture/FrontendIntegrationContract.md) |
| `Engineering/` | Backlog audit, workflow Git dan rilis, gaya Go, audit dependensi | [AuditBacklog.md](Engineering/AuditBacklog.md) |
| `Setup/` | Dev Windows, demo, deploy, runbook operasi, backup, email, monitoring, R2, tooling AI | [DemoReadinessChecklist.md](Setup/DemoReadinessChecklist.md) |
| `Archive/` | Rencana iterasi 4–5, audit lama, evaluasi 2026-10 | Hanya referensi |
