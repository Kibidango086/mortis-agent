package utils

import (
	"path/filepath"
	"strings"
)

// IsPathWithinWorkspace checks if the given path is within the workspace.
// It resolves both paths to absolute paths and checks if the target path
// starts with the workspace path.
func IsPathWithinWorkspace(targetPath, workspace string) bool {
	// Clean and get absolute paths
	absTarget, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return false
	}

	absWorkspace, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return false
	}

	// Add trailing separator to ensure proper prefix matching
	workspacePrefix := absWorkspace + string(filepath.Separator)

	// Check if target is the workspace itself or within it
	return absTarget == absWorkspace || strings.HasPrefix(absTarget, workspacePrefix)
}
