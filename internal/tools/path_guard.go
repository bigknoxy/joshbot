// Package tools provides the tool system for joshbot's agent.
package tools

import (
	"path/filepath"
	"strings"
)

// isWithinBase returns true if path is inside base (or equal), after cleaning.
func isWithinBase(path, base string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && rel != "")
}
