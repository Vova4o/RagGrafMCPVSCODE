# Changelog

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
