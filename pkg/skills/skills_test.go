package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadParsesFrontmatter(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "summarize")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: summarize
description: Summarize long material into concise notes.
---
# Summarize
Detailed instructions.
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	found, err := Load([]string{root})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("skill count = %d, want 1", len(found))
	}
	if found[0].Name != "summarize" {
		t.Fatalf("name = %q, want summarize", found[0].Name)
	}
	if found[0].Description != "Summarize long material into concise notes." {
		t.Fatalf("description = %q", found[0].Description)
	}
}

func TestLoadFollowsSymlinkedSkillDirectory(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target-skill")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(`# Linked Skill
Loads through a symlinked directory.
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	linkDir := filepath.Join(root, "linked-skill")
	if err := os.Symlink(targetDir, linkDir); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	found, err := Load([]string{root})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("skill count = %d, want 2", len(found))
	}

	var linked Skill
	for _, skill := range found {
		if skill.Path == filepath.Join(linkDir, "SKILL.md") {
			linked = skill
			break
		}
	}
	if linked.Path == "" {
		t.Fatalf("missing symlinked skill in %#v", found)
	}
	if linked.Name != "linked-skill" {
		t.Fatalf("name = %q, want linked-skill", linked.Name)
	}
	if linked.Description != "Loads through a symlinked directory." {
		t.Fatalf("description = %q", linked.Description)
	}
}

func TestBuildSummaryIncludesSkillPath(t *testing.T) {
	summary := BuildSummary([]Skill{{
		Name:        "agent-browser",
		Description: "Drive a browser for automation tasks.",
		Path:        "/tmp/skills/agent-browser/SKILL.md",
	}})

	if !strings.Contains(summary, "# Skills") {
		t.Fatalf("summary missing header: %q", summary)
	}
	if !strings.Contains(summary, "agent-browser") {
		t.Fatalf("summary missing skill name: %q", summary)
	}
	if !strings.Contains(summary, "/tmp/skills/agent-browser/SKILL.md") {
		t.Fatalf("summary missing skill path: %q", summary)
	}
}
