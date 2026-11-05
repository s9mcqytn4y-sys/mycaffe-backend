# Curated 48-Repo Toolchain & Skill Catalog (`memory/TOOLCHAIN_CATALOG.md`)

Dokumen ini mengindeks 48+ repository dan skill pilihan Direktur berdasarkan **3-Tier Context Budget Policy** serta audit keamanan `/007` (`audit-skills`).

---

## Tier 1 — Core Auto-Invoked Master Skills (Terpasang di `~/.claude/skills/` & `~/.gemini/config/skills/`)
Enam skill utama ini aktif otomatis berdasarkan konteks tugas tanpa perlu slash command manual:

| Skill Name | Domain & Cakupan | Sumber Best-Practice yang Disatukan |
| :--- | :--- | :--- |
| **`vibe-coder-pro`** | SaaS & Fullstack Engineering (Go/Gin, Node/TS, Next.js App Router, PostgreSQL 18 RLS, Midtrans Core API, Docker) | `andrej-karpathy-skills`, `vercel-labs/skills`, `skill-point-of-sale`, `eCommerce-Skills`, `eng-kit`, `claude-code-best-practice`, `pro-workflow` |
| **`blender-roblox-pipeline`** | Roblox Studio Luau/Rojo modular + Blender 5.2.3 LTS headless Python (`bpy`) -> `.glb`/`.fbx` low-poly & PBR | `brockmartin/roblox-game-skill`, `abhishekbarali-roblox-ui`, `rbsmithy-roblox-claude-skill`, `EL4CTEO/roblox-skills`, `arjun988/blender-skills`, `kevinbadi/blender-skills`, `ellmos-ai-rojo` |
| **`human-writer-content`** | Storytelling & copywriting anti-kaku, script video AI/Tech, hook sosial media, content repurposing | `blader/humanizer`, `antislop`, `copywriting`, `JuliusBrussee/caveman` |
| **`bizdev-client-hunter`** | Go-To-Market (GTM), proposal freelance konversi tinggi, cold outreach, LinkedIn/B2B, pricing strategy, pitch deck | `sergebulaev/linkedin-skills`, `msitarzewski/agency-agents`, `startup-analyst`, `boraoztunc/skills` |
| **`netops-cloudflare-docker`** | Cloudflare Named Tunnels/Zero Trust, R2, DNS, Nginx, Docker Compose, VPS Hardening, Windows/Linux NetOps | `cloudflare/skills`, `cloudflare/security-audit-skill`, `arsallls/claude-network-skills`, `omkamal/vps-setup-skill`, `akin-ozer/cc-devops-skills`, `linux-sysadmin-skills` |
| **`claude-artifact-builder`** | Single-file React/Tailwind/Lucide/Recharts UI interaktif, kalkulator ROI/bisnis, visual prototype | `web-artifacts-builder`, `ui-ux-pro-max`, `remotion-best-practices` |

---

## Tier 2 — Installed Specialized Skills & Plugins (Aktif di Workstation)
- **Token & Output Compression**: `rtk` (`https://github.com/rtk-ai/rtk.git`) — aktif via `PreToolUse` hook di `~/.claude/settings.json` + `caveman` mode ultra-ringkas.
- **Engineering & Security**: `superpowers` (`obra/superpowers`), `karpathy-coder` (`multica-ai/andrej-karpathy-skills`), `security-review`, `release-readiness`, `007`, `audit-skills`, `claude-settings-audit`.
- **Media & Video Code**: `remotion` & `remotion-best-practices` (`remotion-dev/remotion`).
- **Web Extraction**: `defuddle` CLI (`npm i -g defuddle`) & `web-scraper`.
- **Data & Analytics**: `data-analytics-skills` (`mpuig/data-analytics-skills`), `postgres`, `neon-postgres`.

---

## Tier 3 — On-Demand Standalone Frameworks & Tools (Clone/Run Saat Dibutuhkan)
Aplikasi mandiri di bawah ini memiliki *dependency tree* besar (Rust/C++/Python ML/Electron) sehingga **tidak dimasukkan ke prompt global** agar *context window* tetap hemat:

| Repository | Kategori | Cara Pakai On-Demand |
| :--- | :--- | :--- |
| `https://github.com/jamiepine/voicebox.git` | AI Voice Synthesis / Studio | Jalankan di container/venv terpisah saat produksi sulih suara konten video. |
| `https://github.com/webadderallorg/Recordly.git` | Screen Recording & Demo Video | Gunakan untuk merekam demo produk SaaS / MyCaffe POS setelah UI jalan. |
| `https://github.com/lightpanda-io/browser.git` | Ultra-Fast Headless Browser (Zig/Nim) | Gunakan untuk scraping berkecepatan tinggi; sehari-hari gunakan MCP `playwright` / `defuddle`. |
| `https://github.com/ScrapeGraphAI/Scrapegraph-ai.git` | LLM Graph Web Scraper (Python) | Jalankan via `uv run --with scrapegraphai` pada script khusus riset market. |
| `https://github.com/Panniantong/Agent-Reach.git` | Multi-Platform Social Reach Agent | Gunakan pada workflow otomasi distribusi konten / cold outreach. |
| `https://github.com/storytold/artcraft.git` | Visual / Generative Art Pipeline | Referensi pipeline aset visual & shader. |
| `https://github.com/morluto/rea.git` | Agentic Reasoning / Execution | Referensi pola orkestrasi sub-agent. |
| `https://github.com/ethanplusai/harvey.git` | Legal / Contract Review AI | Referensi bersama skill `lex` untuk peninjauan kontrak klien freelance. |
| `https://github.com/google/artemis.git` | Google Security / Testing | Referensi pengujian keamanan otomatis. |
| `https://github.com/mex-memory/mex.git` & `https://github.com/specstoryai/getspecstory.git` | Session History & Memory Archiver | Digantikan oleh arsitektur ringan `./memory/*.md` + MCP `@modelcontextprotocol/server-memory`. |
| `https://github.com/jiweiyeah/Skills-Manager.git` | Skill Directory Manager | Digantikan oleh plugin `antigravity-skills-manager` yang sudah terinstal. |
| `https://github.com/openai/codex.git` | OpenAI CLI Agent | Referensi lintas-CLI; eksekutor utama di laptop ini adalah `claude` (`2.1.302`) dan `agy` (`1.2.5`). |

