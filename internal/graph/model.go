// Package graph defines the persisted code graph model.
package graph

import "time"

const SchemaVersion = 2

// IndexerVersion changes whenever indexing semantics change, so graphs built by
// an older algorithm are treated as stale even when no source file changed.
const IndexerVersion = 3

const (
	KindWorkspace  = "Workspace"
	KindRepository = "Repository"
	KindProject    = "Project"
	KindPackage    = "Package"
	KindFile       = "File"
	KindFunction   = "Function"
	KindMethod     = "Method"
	KindType       = "Type"
	KindExternal   = "External"
)

const (
	EdgeContains   = "CONTAINS"
	EdgeDefines    = "DEFINES"
	EdgeImports    = "IMPORTS"
	EdgeCalls      = "CALLS"
	EdgeReferences = "REFERENCES"
	EdgeDependsOn  = "DEPENDS_ON"
	EdgeImplements = "IMPLEMENTS"
	EdgeExtends    = "EXTENDS"
)

// Node is a symbol or structural element in a repository.
type Node struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Package       string `json:"package,omitempty"`
	File          string `json:"file,omitempty"`
	StartLine     int    `json:"start_line,omitempty"`
	EndLine       int    `json:"end_line,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Language      string `json:"language,omitempty"`
}

// Edge is a directed relation between two nodes.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	// Resolution is ResolutionTyped when a type checker proved the edge; empty
	// means the edge was inferred from names and should be verified in source.
	Resolution string `json:"resolution,omitempty"`
}

// ResolutionTyped marks an edge proven by type information.
const ResolutionTyped = "typed"

// SkippedFile records a source file that could not be indexed.
type SkippedFile struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// Coverage describes the repository content considered by the indexer.
type Coverage struct {
	SupportedExtensions []string      `json:"supported_extensions"`
	IndexedFiles        int           `json:"indexed_files"`
	SkippedFiles        []SkippedFile `json:"skipped_files,omitempty"`
	// DegradedFiles were indexed with name-based call resolution because type
	// information was unavailable for them.
	DegradedFiles     []SkippedFile  `json:"degraded_files,omitempty"`
	IndexedByLanguage map[string]int `json:"indexed_by_language,omitempty"`
}

// Graph is one persisted repository index.
type Graph struct {
	SchemaVersion int       `json:"schema_version"`
	ProjectID     string    `json:"project_id"`
	Name          string    `json:"name"`
	Root          string    `json:"root"`
	Module        string    `json:"module"`
	IndexedAt     time.Time `json:"indexed_at"`
	// SourceFingerprint identifies the on-disk inputs the graph was built from.
	SourceFingerprint string   `json:"source_fingerprint,omitempty"`
	Nodes             []Node   `json:"nodes"`
	Edges             []Edge   `json:"edges"`
	Coverage          Coverage `json:"coverage"`
	Dependencies      []string `json:"dependencies,omitempty"`
	ImportPaths       []string `json:"import_paths,omitempty"`
	URLHosts          []string `json:"url_hosts,omitempty"`
}

// ProjectSummary is the compact form returned when listing indexes.
type ProjectSummary struct {
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Root      string    `json:"root"`
	Module    string    `json:"module"`
	IndexedAt time.Time `json:"indexed_at"`
	Nodes     int       `json:"nodes"`
	Edges     int       `json:"edges"`
}

// Summary returns compact metadata for a graph.
func (g *Graph) Summary() ProjectSummary {
	return ProjectSummary{
		ProjectID: g.ProjectID,
		Name:      g.Name,
		Root:      g.Root,
		Module:    g.Module,
		IndexedAt: g.IndexedAt,
		Nodes:     len(g.Nodes),
		Edges:     len(g.Edges),
	}
}
