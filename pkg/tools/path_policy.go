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

// ResolvePath returns the absolute path if the policy allows it.
func (p PathPolicy) ResolvePath(rawPath string) (string, error) {
	if strings.TrimSpace(rawPath) == "" {
		return "", fmt.Errorf("path is empty")
	}

	switch p.Scope {
	case "", PathScopeWorkspace:
		root, err := p.singleRoot("workspace")
		if err != nil {
			return "", err
		}
		return resolveWithinRoot(root, rawPath)
	case PathScopeAny:
		return resolveAnyPath(rawPath)
	case PathScopeExplicit:
		return p.resolveWithinRoots(rawPath)
	default:
		return "", fmt.Errorf("unsupported path scope %q", p.Scope)
	}
}

func (p PathPolicy) resolveWithinRoots(rawPath string) (string, error) {
	if len(p.Roots) == 0 {
		return "", fmt.Errorf("explicit path scope requires at least one root")
	}

	var lastErr error
	for _, root := range p.Roots {
		resolved, err := resolveWithinRoot(root, rawPath)
		if err == nil {
			return resolved, nil
		}
		lastErr = err
	}

	if filepath.IsAbs(rawPath) {
		if allowed := p.matchAbsoluteRoot(rawPath); allowed != "" {
			return filepath.Abs(rawPath)
		}
	}

	if lastErr != nil {
		return "", fmt.Errorf("path %q is outside the configured roots: %w", rawPath, lastErr)
	}
	return "", fmt.Errorf("path %q is outside the configured roots", rawPath)
}

func (p PathPolicy) matchAbsoluteRoot(rawPath string) string {
	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		return ""
	}

	for _, root := range p.Roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, absPath)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return absRoot
		}
	}
	return ""
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

func resolveWithinRoot(rootDir, rawPath string) (string, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return "", fmt.Errorf("could not resolve root %q: %w", rootDir, err)
	}

	var absPath string
	if filepath.IsAbs(rawPath) {
		absPath, err = filepath.Abs(filepath.Clean(rawPath))
	} else {
		absPath, err = filepath.Abs(filepath.Join(absRoot, filepath.Clean(rawPath)))
	}
	if err != nil {
		return "", fmt.Errorf("could not resolve path %q: %w", rawPath, err)
	}

	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("could not validate path %q: %w", rawPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes root %q", rawPath, absRoot)
	}
	return absPath, nil
}
