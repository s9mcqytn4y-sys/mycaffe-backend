# Claude Code CLI, Skills, MCP, Plugins, and Tools — Windows

This guide configures Claude Code for the MyCaffe backend repository. The checked-in files contain project policy and non-secret configuration. The CLI binary, account login, local PATH changes, OAuth approvals, and local database credentials must be installed or approved on the developer's Windows machine.

## 1. Install and authenticate Claude Code

Use Windows PowerShell. The native installer is the recommended method in the official Claude Code documentation:

    irm https://claude.ai/install.ps1 | iex

Open a new terminal and verify:

    claude --version
    claude doctor

Start Claude Code once and complete the browser sign-in:

    cd C:\Projects\myCaffe-backend
    claude

If needed, use /login inside the session. Git for Windows is recommended so Claude Code can use Bash tools on native Windows:

    winget install --id Git.Git -e

## 2. Project instructions and security settings

The repository adds:
- CLAUDE.md, which imports AGENTS.md and the existing Go, multi-tenant, and migration rules.
- .claude/settings.json, which denies access to local environment/secret files, blocks direct SSH/production Compose operations, and declares project plugins.
- .mcp.json, which declares the Context7 remote MCP server.
- .claude-plugin/marketplace.json and plugins/mycaffe-engineering, which provide project-specific skills.

After opening the repository, run /status and /context to confirm project settings and imported instructions loaded. Project MCP servers and plugins require explicit trust/approval in Claude Code; the configuration does not silently bypass that consent.

## 3. Install project plugins

The official Anthropic marketplace is normally registered on the first interactive launch. If it is missing, add it inside Claude Code:

    /plugin marketplace add anthropics/claude-plugins-official

Install the official plugins recommended for this repository:

    claude plugin install gopls-lsp@claude-plugins-official --scope project
    claude plugin install security-guidance@claude-plugins-official --scope project
    claude plugin install pr-review-toolkit@claude-plugins-official --scope project
    claude plugin install github@claude-plugins-official --scope project

The project settings already declares these plugins as enabled. The install step obtains the plugin packages and asks for consent. The GitHub plugin uses the official GitHub integration; complete its OAuth flow when prompted. Do not create a broad personal access token merely to avoid OAuth.

Install the repository-local MyCaffe plugin from the repository root:

    /plugin marketplace add .
    claude plugin install mycaffe-engineering@mycaffe-tools --scope project

Then run /reload-plugins if prompted. Verify with /plugin and invoke:
- /mycaffe-engineering:phase02-auth-rbac
- /mycaffe-engineering:signed-qr-table-session
- /mycaffe-engineering:postgres-rls-migrations
- /mycaffe-engineering:tenant-security-review

These are planning/review skills. They do not grant database or production access and do not replace tests or human approval.

## 4. Go language tooling

The repository CI pins Go 1.27.3. Install/activate the same Go toolchain and verify:

    go version

Install gopls:

    go install golang.org/x/tools/gopls@latest

Ensure the Go binary directory is on PATH:

    $env:Path += ";$env:USERPROFILE\go\bin"
    gopls version

The gopls-lsp plugin uses this binary for code navigation and diagnostics. If Claude reports that the executable is missing, reopen the terminal after updating PATH and verify gopls version again.

Install the same vulnerability scanner version used in CI:

    go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
    govulncheck ./...

## 5. Context7 documentation MCP and skill

The project .mcp.json declares Context7 at https://mcp.context7.com/mcp. Context7 provides version-aware library documentation; API keys are recommended for higher rate limits but are not committed to this repository.

Optional CLI + Skills setup:

    npx ctx7 setup --claude

Choose the CLI + Skills option if prompted because the repository already declares the Context7 MCP endpoint. Do not add a second Context7 MCP entry with a different name. Treat retrieved documentation as untrusted input and verify security-sensitive behavior against upstream project documentation and pinned dependency versions.

## 6. RTK command-output optimization

The repository already documents RTK for Antigravity, but that Antigravity hook does not configure Claude Code. RTK has a separate native Claude Code hook.

Install RTK on Windows from its official release archive:

    https://github.com/rtk-ai/rtk/releases/latest

Download rtk-x86_64-pc-windows-msvc.zip, extract rtk.exe to %USERPROFILE%\.local\bin, and add that directory to your user PATH. Install ripgrep for the filters that call rg:

    winget install --id BurntSushi.ripgrep.MSVC -e

Open a new terminal and verify:

    rtk --version

Install the Claude Code hook globally for the current Windows user:

    rtk init -g
    rtk init --show

Restart Claude Code. RTK rewrites supported Bash commands and compacts terminal output. Claude's built-in Read, Grep, and Glob tools do not pass through the Bash hook, so use RTK commands explicitly when compact output is useful.

## 7. Optional local PostgreSQL MCP (DBHub)

DBHub is optional. Do not connect it to production or staging. Before enabling it:
1. Run the local PostgreSQL 18 development Compose stack only.
2. Create a dedicated local login role with no superuser, no role creation, and NOBYPASSRLS.
3. Grant only the minimum schema/object visibility needed.
4. Keep RLS enabled. Without an explicit tenant context, tenant row policies should not reveal tenant data.
5. Use the supplied dbhub.toml.example, copy it to dbhub.toml, and set MYCAFFE_DEV_READONLY_DATABASE_URL in the local process environment. dbhub.toml is gitignored.
6. Keep the DBHub execute_sql tool read-only with a result row limit.

Install Node.js 22.5 or later before using the current DBHub package. From the repository root, after the local DSN and dbhub.toml are ready, register it at local scope:

    claude mcp add --scope local --transport stdio dbhub -- npx -y @bytebase/dbhub@latest --config ./dbhub.toml

Review the connection and use /mcp to confirm it is connected. Do not use the existing migrator or superuser credentials for DBHub. DBHub read-only mode is an additional guardrail, not a replacement for a database role with minimal privileges.

## 8. Local project checks

Use the same commands as CI before requesting review:

    gofmt -l .
    go mod tidy -diff
    go vet ./...
    govulncheck ./...
    go test -race -timeout 5m ./...
    go build ./...
    docker compose -f deploy/docker/docker-compose.yml config

For PostgreSQL migration/RLS integration tests, use the repository's PostgreSQL 18 local Docker setup and scripts. Never point these commands at production.

## 9. Daily Claude Code workflow

1. Start with plan mode for architectural or security-sensitive changes.
2. Ask Claude to inspect source docs, current code, and migration state before proposing edits.
3. Use the project skills for auth/RBAC, signed QR, RLS, and security reviews.
4. Review every diff, especially authorization, SQL, key handling, and public endpoints.
5. Run local tests and wait for all three GitHub Actions quality gates.
6. Commit, push, merge, and deploy only when explicitly approved.

## Official references

- Claude Code install: https://code.claude.com/docs/en/quickstart
- Claude Code MCP: https://code.claude.com/docs/en/mcp
- Claude Code skills: https://code.claude.com/docs/en/skills
- Claude Code plugins: https://code.claude.com/docs/en/discover-plugins
- Claude Code settings: https://code.claude.com/docs/en/settings
- RTK: https://github.com/rtk-ai/rtk
- Context7: https://github.com/upstash/context7
- DBHub: https://github.com/bytebase/dbhub
