package git

import "context"

// Test-only aliases for unexported helpers. Visible to package git_test in this directory,
// not to other packages that import git.
var (
	GitChildEnv          = gitChildEnv
	GitBin               = gitBin
	ParseCommitLogOutput = parseCommitLogOutput
	ParseCatFileBatch    = parseCatFileBatch
	FullObjectID         = fullObjectID
)

// MissingObjects exposes the local presence probe to package git_test.
func (g *EnvironmentOperations) MissingObjects(ctx context.Context, oids ...string) ([]string, error) {
	return g.missingObjects(ctx, oids...)
}
