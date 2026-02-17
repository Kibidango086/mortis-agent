package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SkillInstaller struct {
	workspace string
}

type BuiltinSkill struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

func NewSkillInstaller(workspace string) *SkillInstaller {
	return &SkillInstaller{
		workspace: workspace,
	}
}

func (si *SkillInstaller) Uninstall(skillName string) error {
	skillDir := filepath.Join(si.workspace, "skills", skillName)

	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		return fmt.Errorf("skill '%s' not found", skillName)
	}

	if err := os.RemoveAll(skillDir); err != nil {
		return fmt.Errorf("failed to remove skill: %w", err)
	}

	return nil
}

func (si *SkillInstaller) ListBuiltinSkills() []BuiltinSkill {
	builtinSkillsDir := filepath.Join(filepath.Dir(si.workspace), "mortisagent", "skills")

	entries, err := os.ReadDir(builtinSkillsDir)
	if err != nil {
		return nil
	}

	var skills []BuiltinSkill
	for _, entry := range entries {
		if entry.IsDir() {
			_ = entry
			skillName := entry.Name()
			skillFile := filepath.Join(builtinSkillsDir, skillName, "SKILL.md")

			data, err := os.ReadFile(skillFile)
			description := ""
			if err == nil {
				content := string(data)
				if idx := strings.Index(content, "\n"); idx > 0 {
					firstLine := content[:idx]
					if strings.Contains(firstLine, "description:") {
						descLine := strings.Index(content[idx:], "\n")
						if descLine > 0 {
							description = strings.TrimSpace(content[idx+descLine : idx+descLine])
						}
					}
				}
			}

			status := "✓"
			fmt.Printf("  %s  %s\n", status, entry.Name())
			if description != "" {
				fmt.Printf("    %s\n", description)
			}
		}
	}
	return skills
}
