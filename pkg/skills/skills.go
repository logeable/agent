package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is the minimal metadata the runtime needs from one skill package.
//
// Why:
// The agent should see what skills exist before it decides to read any SKILL.md
// file. Keeping the metadata small avoids bloating the default prompt.
type Skill struct {
	Name        string
	Description string
	Path        string
}

// Load scans each root for child directories that contain a `SKILL.md` file.
//
// Why:
// Skills live outside `agentcore`. This loader gives the upper layer one place
// to discover them without mixing file-system scanning into prompt assembly.
func Load(roots []string) ([]Skill, error) {
	var out []Skill

	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}

		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read skills root %q: %w", root, err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			skillPath := filepath.Join(root, entry.Name(), "SKILL.md")
			data, err := os.ReadFile(skillPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("read skill file %q: %w", skillPath, err)
			}

			out = append(out, parseSkillFile(skillPath, entry.Name(), string(data)))
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Path < out[j].Path
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// BuildSummary renders a compact skills section for the system prompt.
//
// Why:
// The prompt should advertise available capabilities, but the detailed skill
// instructions should remain in SKILL.md until the agent needs them.
func BuildSummary(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	lines := []string{
		"# Skills",
		"The following skills are available. Read a skill's SKILL.md with read_file before relying on it.",
		"",
	}

	for _, skill := range skills {
		line := fmt.Sprintf("- %s", skill.Name)
		if skill.Description != "" {
			line += ": " + skill.Description
		}
		line += fmt.Sprintf(" (%s)", skill.Path)
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func parseSkillFile(path, fallbackName, content string) Skill {
	name := strings.TrimSpace(fallbackName)
	description := ""

	if frontmatter, body, ok := splitFrontmatter(content); ok {
		if parsed := frontmatterValue(frontmatter, "name"); parsed != "" {
			name = parsed
		}
		if parsed := frontmatterValue(frontmatter, "description"); parsed != "" {
			description = parsed
		}
		if description == "" {
			description = firstBodyParagraph(body)
		}
	} else {
		description = firstBodyParagraph(content)
	}

	return Skill{
		Name:        name,
		Description: description,
		Path:        path,
	}
}

func splitFrontmatter(content string) (frontmatter string, body string, ok bool) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content, false
	}
	rest := strings.TrimPrefix(content, "---\n")
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return "", content, false
	}
	return rest[:idx], rest[idx+5:], true
}

func frontmatterValue(frontmatter, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		return strings.Trim(value, `"'`)
	}
	return ""
}

func firstBodyParagraph(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}
