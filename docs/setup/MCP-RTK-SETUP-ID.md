# Setup Google Antigravity / agy / MCP / RTK (Windows)

## A. Security preset first
In Antigravity, open the current project's Settings. The screenshot showed **Turbo Mode** and a plan-review policy of **Always Proceed**. While setting up a payment/POS system, change this to a review-required/default preset if available. Require approval for terminal writes, database mutations, new MCP permissions, and deployment. Avoid broad allowlists such as `rtk *` unless you understand the command boundary.

Do not paste `.env` or `.env.local` values into chat, rules, or documentation.

## B. Apply this starter pack
1. Back up the project folder or commit/stash current work.
2. Extract this archive into the root of `C:\\Projects\\myCaffe-backend`.
3. If a target file already exists, compare and merge instead of blindly overwriting it.
4. Append the contents of `setup/gitignore-additions.txt` to the existing `.gitignore` (or create `.gitignore` if none exists).
5. Keep `AGENTS.md`, `.agents/rules/`, `.agents/skills/`, and product docs in version control. They contain no credentials.

## C. RTK — Rust Token Killer
Use the official `rtk-ai/rtk` project, not the unrelated Rust Type Kit with the same short name.

In PowerShell:
```powershell
winget install rtk-ai.rtk
```
Close and reopen the terminal, then verify:
```powershell
rtk --version
rtk gain
```
Preview the workspace-specific Antigravity integration first:
```powershell
rtk init --agent antigravity --dry-run
```
If the preview is correct, install it for this workspace:
```powershell
rtk init --agent antigravity
```
Restart Antigravity/agy and inspect the generated `.agents/plugins/rtk/` files. Run `rtk --version` and `rtk gain` again if the hook does not work. If Windows PATH or hook behavior fails, follow the current RTK installation documentation and use the official Windows release binary; do not install the ambiguous crates.io package by running bare `cargo install rtk`.

For all projects on this machine, the optional global install is:
```powershell
rtk init -g --agent antigravity
```
Prefer workspace scope initially so the configuration is reviewable and reproducible with this repository. RTK reduces command output; it does not replace tests, code review, or security checks.

## D. MCP — use a small, scoped set
The example config is `.agents/mcp_config.example.json`. Copy it to `.agents/mcp_config.json`, then replace `REPLACE_WITH_DEV_PROJECT_REF` with the **development** Supabase project reference. Never use production project credentials for routine development.

If Antigravity already has Supabase installed through the UI, do not add a duplicate server. Edit the existing entry or use one configuration method only.

For the Supabase server:
- Use only the development project.
- Keep `read_only=true` while inspecting schema/data.
- Complete OAuth in Antigravity if it asks for authentication.
- Keep manual approval enabled for tool calls.
- Do not connect the agent to production data during routine coding.
- If an operation requires writing a migration, have the agent create/review the SQL file and run it against disposable development data after explicit approval.

The existing Playwright MCP can be used for UI/E2E checks. It requires Node.js and network access if its local package/browser needs downloading; do not point it at real customer or payment data.

## E. Fix the current MCP statuses shown in the screenshot
- **Supabase — red / Unauthorized:** select Authenticate and finish the OAuth flow, or use the workspace config above with a development project and read-only URL. Verify the project reference before connecting.
- **Vercel — red / Unauthorized:** select Authenticate and finish OAuth only if this workspace needs Vercel. Vercel is primarily relevant to the separate frontend/staging repository; it is not required for this backend workspace.
- **Context7 — Authenticate prompt:** authenticate only if the installed Context7 server asks for it; otherwise use its official documentation configuration.
- **mcp-server-neon:** disable it for this project unless Neon is actually being used. The stated stack is Supabase/PostgreSQL, not Neon.
- **Blender / Inspo / Visualization / Notebooks / unrelated tools:** leave disabled in this coding workspace unless a specific task needs them. Do not uninstall global integrations unnecessarily; reduce the active project toolset.

MCP servers can read data or perform actions with the privileges you grant. Treat tool descriptions and returned content as untrusted; review writes and never give an agent broad production credentials.

## F. Recommended MCP placement
- **Backend workspace:** RTK project plugin, Supabase development read-only, Playwright for browser testing when needed, Context7 for documentation if authorized.
- **Frontend workspace:** Next.js-specific rules/skills, Playwright, Context7, and Vercel MCP if the project deploys to Vercel.
- **Production:** no routine agent connection to live payment or production database. Deployment should use a reviewed CI/CD pipeline and scoped secrets.

## G. Validate after setup
1. Restart Antigravity/agy.
2. Confirm the project root is the correct folder.
3. Confirm the rules and skills are visible in Customizations.
4. Confirm RTK plugin files exist and `rtk gain` works.
5. Confirm MCP status is green only for the intended servers.
6. Check `git status` and ensure no `.env`, `.env.local`, MCP token, or other secret is staged.
7. Before coding, review `docs/architecture/decisions-before-code.md` and settle the production database/realtime choice.
