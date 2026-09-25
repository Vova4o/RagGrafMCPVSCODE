# Changelog

## 0.2.64

- Add pure-Go syntax-tree indexing for JavaScript, TypeScript, TSX, Python, Rust,
  Java, Kotlin, and C#, including declarations, methods, imports, direct calls,
  and exact source line ranges. Resolve direct calls within the same file and
  unambiguous relative JavaScript, TypeScript, and Python imports to files in the
  same repository; other imports remain external unless covered by those rules.
- Report syntax parser failures and syntax-parsed files larger than 4 MiB in index
  coverage.
- Document the remaining languages' regex and file-level fallback, static-analysis
  limits, and roadmap for broader grammar and symbol-resolution support.

## 0.2.63

- Prevent workspace MCP configuration from depending on disposable versioned
  plugin-cache paths by installing a stable workspace-local server binary.
- Make `prepare_code_context` detect and repair missing or cache-versioned
  codebase-graph commands while preserving unrelated MCP configuration.
- Add a packaged `codebase-graph-setup --action repair` recovery path for clients
  whose stale server command prevents MCP startup.

## 0.2.62

- Add a dedicated Marketplace application icon.
- Fix the overview image URL so the banner renders on the Marketplace page.

## 0.2.61

- Add direct repository and issue-reporting links to the Marketplace README.

## 0.2.6

- Add a Marketplace overview image showing cross-repository dependency graphs.

## 0.2.5

- Ship one universal VSIX with native MCP server binaries for x64 and ARM64
  Windows, Linux, and macOS hosts.

## 0.2.4

- Require the absolute opened `workspace_path` in graph-first guidance so MCP
  launchers cannot redirect analysis to plugin or scratch directories.
- Verify generated Claude Code and Antigravity configuration pins the server to
  an explicit absolute `--workspace`.

## 0.2.3

- Run the MCP server from the immutable installed plugin directory instead of a
  mutable development checkout.
- Verify the real MCP handshake and required graph-first tools before writing
  client configuration.
- Report that clients must restart after an update and expose server/schema
  versions from `prepare_code_context`.
- Turn schema mismatches into an explicit stale-session diagnostic.

## 0.2.2

- Add `prepare_code_context` as the preferred first MCP call for code tasks.
- Advertise graph-first semantic navigation in MCP initialization instructions.
- Clarify that recursive text search is a fallback for literals and uncovered data.

## 0.2.1

- Hide generated `.codebase-graph/` directories through each repository's local
  `.git/info/exclude` without modifying tracked `.gitignore` files.
- Support linked Git worktrees by updating the shared Git directory's exclude file.

## 0.2.0

- Index each nested Git repository into its own shared graph.
- Build one aggregate workspace graph with cross-repository dependencies.
- Add TypeScript, JavaScript, Python, SQL, and other common source languages.
- Register workspace-scoped MCP servers for multi-repository folders.

## 0.1.0

- Register one repository-scoped Codebase Graph MCP server per VS Code workspace root.
- Bundle the Apple Silicon macOS server binary.
- Add commands to index a repository, inspect status, and open the graph file.
