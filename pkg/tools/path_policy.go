package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathScope declares how file tools are allowed to resolve paths.
//
// Why:
// Read/edit/write should share one access policy instead of each tool growing
// its own flags and edge-case behavior.
type PathScope string

const (
	// PathScopeWorkspace restricts access to one workspace root.
	PathScopeWorkspace PathScope = "workspace"

	// PathScopeAny allows any filesystem path.
	PathScopeAny PathScope = "any"

	// PathScopeExplicit allows access only within a fixed set of roots.
	PathScopeExplicit PathScope = "explicit"
)

// PathPolicy controls how file tools resolve and authorize paths.
//
// What:
// A policy converts a caller-provided path into an absolute path and decides
// whether the path is allowed for this agent instance.
//
// Why:
// File access is an instance-level concern, not a per-tool ad hoc rule.
type PathPolicy struct {
	Scope PathScope
	Roots []string
}

// ResolvePathWithEscape reports the resolved absolute path and whether the path
// escapes the configured roots.
//
// Why:
// File tools sometimes need a third outcome besides "allowed" or "denied":
// "this path is outside the normal boundary, so approval may be required".
func (p PathPolicy) ResolvePathWithEscape(rawPath string) (string, bool, error) {
	if strings.TrimSpace(rawPath) == "" {
		return "", false, fmt.Errorf("path is empty")
	}

	switch p.Scope {
	case "", PathScopeWorkspace:
		root, err := p.singleRoot("workspace")
		if err != nil {
			return "", false, err
		}
		return resolveAgainstRoot(root, rawPath)
	case PathScopeAny:
		resolved, err := resolveAnyPath(rawPath)
		return resolved, false, err
	case PathScopeExplicit:
		return p.resolveWithinRootsWithEscape(rawPath)
	default:
		return "", false, fmt.Errorf("unsupported path scope %q", p.Scope)
	}
}

// ResolvePath returns the absolute path if the policy allows it.
func (p PathPolicy) ResolvePath(rawPath string) (string, error) {
	resolved, escaped, err := p.ResolvePathWithEscape(rawPath)
	if err != nil {
		return "", err
	}
	if escaped {
		return "", fmt.Errorf("path %q is outside the configured roots", rawPath)
	}
	return resolved, nil
}

func (p PathPolicy) resolveWithinRootsWithEscape(rawPath string) (string, bool, error) {
	if len(p.Roots) == 0 {
		return "", false, fmt.Errorf("explicit path scope requires at least one root")
	}

	for _, root := range p.Roots {
		resolved, escaped, err := resolveAgainstRoot(root, rawPath)
		if err == nil {
			if !escaped {
				return resolved, false, nil
			}
		}
	}

	candidate, _, err := resolveAgainstRoot(p.Roots[0], rawPath)
	if err != nil {
		return "", false, err
	}
	return candidate, true, nil
}

func (p PathPolicy) singleRoot(label string) (string, error) {
	if len(p.Roots) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("could not resolve default %s root: %w", label, err)
		}
		return cwd, nil
	}
	return p.Roots[0], nil
}

func resolveAnyPath(rawPath string) (string, error) {
	return filepath.Abs(filepath.Clean(rawPath))
}

func resolveAgainstRoot(rootDir, rawPath string) (string, bool, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return "", false, fmt.Errorf("could not resolve root %q: %w", rootDir, err)
	}

	var absPath string
	if filepath.IsAbs(rawPath) {
		absPath, err = filepath.Abs(filepath.Clean(rawPath))
	} else {
		absPath, err = filepath.Abs(filepath.Join(absRoot, filepath.Clean(rawPath)))
	}
	if err != nil {
		return "", false, fmt.Errorf("could not resolve path %q: %w", rawPath, err)
	}

	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", false, fmt.Errorf("could not validate path %q: %w", rawPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return absPath, true, nil
	}
	return absPath, false, nil
}
