# Codebase Graph for VS Code

This extension provides VS Code agents with the `codebase-graph` MCP server. It
indexes supported source languages into one graph per actual Git repository and
one aggregate graph per opened workspace.

[GitHub repository](https://github.com/Vova4o/RagGrafMCPVSCODE) · [Report an issue](https://github.com/Vova4o/RagGrafMCPVSCODE/issues/new)

![Codebase Graph visualizing cross-repository dependencies](https://raw.githubusercontent.com/Vova4o/RagGrafMCPVSCODE/main/vscode-extension/media/codebase-graph-marketplace-v2.png)

```text
<workspace>/<repo>/.codebase-graph/graph.json
<workspace>/.codebase-graph/workspace.json
```

All agents share those files. The extension does not create agent-specific indexes
and does not run a daemon or watcher.

## Cross-repository dependencies

The workspace graph adds `DEPENDS_ON` when an indexed typed import names the
target repository's exact module path (or a path beneath it), or when source code
contains an HTTP(S) URL whose exact host is mapped to that repository. Substring
matches in repository names, SQL table names, and asset names do not create edges.

To map service hosts, create `codebase-graph.services.json` in the workspace root:

```json
{
  "services": [
    { "repository": "api", "hosts": ["api.example.test:8080"] }
  ]
}
```

`repository` is a path relative to the workspace root. List the exact URL host;
include its port when the source URL uses one. These edges record static source
evidence and do not trace HTTP requests at runtime. URLs assembled from variables
and actual service-to-service traffic are not detected.

## Language coverage

Go uses the standard-library AST. JavaScript, TypeScript, TSX, Python, Rust, Java,
Kotlin, and C# use pure-Go syntax-tree parsing for declarations, methods, imports,
direct calls, and exact source line ranges, with conservative same-repository
resolution limited to same-file direct calls and unambiguous relative JavaScript,
TypeScript, and Python imports to files in that repository. Other imports remain
external unless covered by those rules. SQL, shell, Ruby, PHP, C, C++, Protocol
Buffers, Vue, Svelte, HTML, CSS, SCSS, and configuration formats use older regex and
file-level fallback extraction. Dynamic dispatch, reflection, runtime loading, and
ambiguous calls remain external; syntax parser failures and syntax-parsed files larger
than 4 MiB appear in index coverage. Broader cross-file and cross-repository symbol
resolution and grammar-based support for the remaining languages are planned.

## Commands

- `Codebase Graph: Index Workspace`
- `Codebase Graph: Show Index Status`
- `Codebase Graph: Open Workspace Graph`

In a multi-root VS Code window, select the workspace root to operate on. Each local
workspace root gets one workspace-scoped MCP server, which discovers its internal
repositories automatically.

The universal package contains native server binaries for x64 and ARM64 Windows,
Linux, and macOS hosts. VS Code selects the matching binary at runtime.
