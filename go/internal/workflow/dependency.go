package workflow

import (
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

// validTaskID reuses the Task Domain's own canonical ParseTaskID instead of
// an independent regex, so a Task ID Dependencies Markdown accepts is
// guaranteed to be the same one the rest of the system accepts (previously
// this package had its own looser ^TASK-\d+$ pattern that could disagree
// with task.ParseTaskID's stricter zero-padded, round-tripping format).
func validTaskID(taskID string) bool {
	_, err := task.ParseTaskID(taskID)
	return err == nil
}

// Dependency expresses task-to-task dependencies without storage concerns.
type Dependency struct {
	TaskID    string   `json:"task_id"`
	DependsOn []string `json:"depends_on"`
}

// ParseDependencies converts Task Dependencies Markdown into domain values.
// It deliberately accepts a string so file I/O remains outside the Go Core.
func ParseDependencies(markdown string) ([]Dependency, error) {
	dependencies := make([]Dependency, 0)
	seen := make(map[string]struct{})

	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
			continue
		}

		cells := splitTableCells(trimmed)
		if len(cells) == 0 || cells[0] == "Task ID" || cells[0] == "---" {
			continue
		}
		if len(cells) < 3 {
			return nil, fmt.Errorf("invalid dependency table row: %q", line)
		}

		taskID := cells[0]
		if !validTaskID(taskID) {
			return nil, fmt.Errorf("invalid task ID in dependency table: %s", taskID)
		}
		if _, exists := seen[taskID]; exists {
			return nil, fmt.Errorf("duplicate task ID in dependency table: %s", taskID)
		}
		seen[taskID] = struct{}{}

		dependsOn := make([]string, 0)
		if cells[2] != "" && cells[2] != "なし" {
			for _, rawID := range strings.Split(cells[2], ",") {
				dependencyID := strings.TrimSpace(rawID)
				if dependencyID == "" {
					continue
				}
				if !validTaskID(dependencyID) {
					return nil, fmt.Errorf(
						"invalid dependency ID in dependency table: %s",
						dependencyID,
					)
				}
				dependsOn = append(dependsOn, dependencyID)
			}
		}

		dependencies = append(dependencies, Dependency{
			TaskID:    taskID,
			DependsOn: dependsOn,
		})
	}

	return dependencies, nil
}

func splitTableCells(line string) []string {
	body := strings.Trim(strings.TrimSpace(line), "|")
	cells := make([]string, 0)
	var cell strings.Builder
	escaped := false

	for _, char := range body {
		if char == '|' && !escaped {
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
			continue
		}
		cell.WriteRune(char)
		if char == '\\' && !escaped {
			escaped = true
		} else {
			escaped = false
		}
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	return cells
}
