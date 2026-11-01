# Go Module Dependencies & Architecture Audit

Dokumen ini merekam hasil audit formal dependensi modul Go (`go.mod`, `go.sum`), pemeriksaan integritas checksum (*checksum verification*), analisis kerentanan keamanan (*security vulnerability audit*), dan evaluasi lisensi pada repositori `mycaffe-backend`.

---

## 1. Verifikasi Modul Aktual

Audit dieksekusi pada environment lokal:

```bash
go version: go 1.27.3 windows/amd64
go mod verify: all modules verified
go mod tidy -diff: 0 diff (bersih)
govulncheck ./...: 0 vulnerabilities found affecting code
```

---

## 2. Inventarisasi Dependensi Langsung (*Direct Dependencies*)

| Modul | Versi | Tanggung Jawab / Fitur | Evaluasi Arsitektural | Lisensi | Status Keputusan |
| ------- | ------- | ------------------------ | ----------------------- | --------- | ------------------ |
| `github.com/gin-gonic/gin` | `v1.12.0` | HTTP Web Framework, route routing, middleware context | Performa tinggi, API ergonomis, mature | MIT | **Pertahankan** |
| `github.com/jackc/pgx/v5` | `v5.11.0` | Driver PostgreSQL natif berperforma tinggi, pgtype, TCP pooling | Driver standar industri untuk PostgreSQL 18 | MIT | **Pertahankan** |
| `github.com/jmoiron/sqlx` | `v1.4.0` | Ekstensi `database/sql` untuk struct scanning & named queries | Ringan, tanpa overhead query planner ORM | MIT | **Pertahankan** |
| `github.com/google/uuid` | `v1.6.0` | Generasi dan parsing UUIDv4 untuk ID entitas tenant | Standar baku Google untuk UUID | BSD-3-Clause | **Pertahankan** |
| `github.com/golang-jwt/jwt/v5` | `v5.3.1` | Autentikasi token JWT (access token staf kafe) | Library resmi de facto Go JWT v5 | MIT | **Pertahankan** |
| `golang.org/x/crypto` | `v0.58.0` | Argon2id hashing untuk PIN staf & password | Standar kriptografi resmi Go Team | BSD-3-Clause | **Pertahankan** |
| `golang.org/x/term` | `v0.47.0` | Masking input terminal untuk CLI migrator PIN prompt | Paket standar Go | BSD-3-Clause | **Pertahankan** |
| `github.com/gorilla/websocket` | `v1.5.3` | Hub komunikasi realtime KDS, POS, dan TV antrean | Implementasi RFC 6455 yang sangat stabil | BSD-2-Clause | **Pertahankan** |
| `github.com/midtrans/midtrans-go` | `v1.3.8` | SDK resmi integrasi Midtrans Core API & QRIS | Gateway pembayaran resmi Indonesia | MIT | **Pertahankan di balik interface gateway** |
| `github.com/getsentry/sentry-go` | `v0.50.0` | Error monitoring & crash reporting | Pelaporan error real-time ke Sentry cloud | Apache-2.0 | **Pertahankan; lengkapi middleware** |
| `github.com/joho/godotenv` | `v1.5.1` | Pemuat variabel `.env` pada lingkungan pengembangan lokal | Mudah digunakan untuk dev lokal | MIT | **Pertahankan untuk local dev only** |

---

## 3. Evaluasi Dependensi Tak Langsung (*Indirect Dependencies*)

Dependensi tak langsung yang ditarik oleh framework Gin, pgx, dan parser:

- `github.com/bytedance/sonic`: Parser JSON cepat berbasis Assembly (digunakan oleh Gin).
- `github.com/go-playground/validator/v10`: Engine validasi struct tag.
- `golang.org/x/net`, `golang.org/x/sys`, `golang.org/x/text`: Sub-repositori inti bahasa Go.
- `go.mongodb.org/mongo-driver/v2`: Ditarik secara transitif oleh Sentry Go tracer untuk monitoring driver MongoDB opsional (tidak ada kode MyCaffe yang memanggilnya).

---

## 4. Analisis Usulan Dependensi Baru (Evaluasi Kritis)

Sesuai prinsip arsitektur *Single-VPS Modular Monolith* (ADR-002) dan aturan *Anti-AI-Slop*:

1. **Penolakan ORM Baru (GORM / Ent / Prisma)**:
   - **Keputusan**: Ditolak.
   - **Alasan**: PostgreSQL 18 RLS, transactional tenant context (`database.WithTenantTx`), composite foreign keys, dan strict lock ordering (`FOR UPDATE`) jauh lebih aman, transparan, dan dapat diaudit jika ditulis menggunakan SQL murni berparameter via `sqlx` + `pgx/v5`.
2. **Penolakan Message Broker Eksternal (Kafka / RabbitMQ / Redis PubSub)**:
   - **Keputusan**: Ditolak.
   - **Alasan**: Arsitektur saat ini menggunakan tabel `outbox_events` di dalam PostgreSQL dengan *transactional outbox pattern* dan in-memory WebSocket Hub (`internal/realtime/hub.go`). Menambah broker eksternal menambah titik kegagalan (*single point of failure*) dan overhead memori pada VPS Hostinger KVM 2.
3. **Penolakan Caching Layer Eksternal (Redis / Dragonfly)**:
   - **Keputusan**: Ditolak untuk fase MVP.
   - **Alasan**: Query katalog kafe telah dioptimalkan dengan indeks komposit dan dieksekusi di bawah 2 milidetik oleh engine PostgreSQL 18.
4. **Penolakan Dependency Injection Framework (Wire / Uber Digi)**:
   - **Keputusan**: Ditolak.
   - **Alasan**: Konstruktor Go standar (`NewOrderService(...)`, `NewCatalogService(...)`) sangat jelas, eksplisit, mudah di-trace oleh developer, dan tidak membutuhkan kompilasi kode tambahan (*code generation*).

---

## 5. Ringkasan Keamanan & Kepatuhan Lisensi

- Seluruh dependensi langsung menggunakan lisensi permisif open-source (**MIT**, **BSD-2-Clause**, **BSD-3-Clause**, **Apache-2.0**).
- Tidak ada modul yang menggunakan lisensi Copyleft agresif (GPL/AGPL) yang berisiko mengikat lisensi kode proprietary MyCaffe Core POS.
- `govulncheck` mengonfirmasi 0 kerentanan yang berdampak pada fungsi yang dipanggil oleh aplikasi.
