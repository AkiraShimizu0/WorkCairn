package vault

import (
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/workflow"
)

func parseFrontmatter(content string) (map[string]string, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("%w: frontmatter start", ErrInvalidDocument)
	}
	values := make(map[string]string)
	closed := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "" {
			values[key] = strings.TrimSpace(value)
		}
	}
	if !closed {
		return nil, fmt.Errorf("%w: frontmatter end", ErrInvalidDocument)
	}
	return values, nil
}

func parseProjectOverview(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	start := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == "## 概要" {
			start = index + 1
			break
		}
	}
	if start == -1 {
		return ""
	}
	end := len(lines)
	for index := start; index < len(lines); index++ {
		if strings.HasPrefix(strings.TrimSpace(lines[index]), "## ") {
			end = index
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

func parseTasks(content string) ([]workflow.Task, error) {
	headerFound := false
	seen := make(map[string]struct{})
	tasks := make([]workflow.Task, 0)
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		cells, ok := tableCells(line)
		if !ok || len(cells) != 5 {
			continue
		}
		if cells[0] == "ID" {
			if cells[3] != "担当社員ID" {
				return nil, fmt.Errorf("%w: legacy assignee column", ErrInvalidDocument)
			}
			headerFound = true
			continue
		}
		if cells[0] == "---" {
			continue
		}
		if _, err := task.ParseTaskID(cells[0]); err != nil {
			return nil, fmt.Errorf("%w: Task ID", ErrInvalidDocument)
		}
		if _, exists := seen[cells[0]]; exists {
			return nil, fmt.Errorf("%w: duplicate Task ID", ErrInvalidDocument)
		}
		seen[cells[0]] = struct{}{}
		status := task.Status(cells[2])
		if strings.TrimSpace(cells[1]) == "" || !status.Valid() {
			return nil, fmt.Errorf("%w: Task fields", ErrInvalidDocument)
		}
		var assigneeID *string
		if cells[3] != "未割当" && cells[3] != "" {
			value := cells[3]
			assigneeID = &value
		}
		tasks = append(tasks, workflow.Task{
			ID: cells[0], Title: cells[1], Status: cells[2], AssigneeID: assigneeID,
		})
	}
	if !headerFound {
		return nil, fmt.Errorf("%w: Tasks table header", ErrInvalidDocument)
	}
	return tasks, nil
}

func parseDependencies(content string) ([]workflow.Dependency, error) {
	seen := make(map[string]struct{})
	dependencies := make([]workflow.Dependency, 0)
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		cells, ok := escapedTableCells(line)
		if !ok {
			continue
		}
		if len(cells) < 3 {
			return nil, fmt.Errorf("%w: dependency table row", ErrInvalidDocument)
		}
		if cells[0] == "Task ID" || cells[0] == "---" {
			continue
		}
		if _, err := task.ParseTaskID(cells[0]); err != nil {
			return nil, fmt.Errorf("%w: dependency Task ID", ErrInvalidDocument)
		}
		if _, exists := seen[cells[0]]; exists {
			return nil, fmt.Errorf("%w: duplicate dependency Task ID", ErrInvalidDocument)
		}
		seen[cells[0]] = struct{}{}
		dependsOn := make([]string, 0)
		if cells[2] != "" && cells[2] != "なし" {
			for _, dependencyID := range strings.Split(cells[2], ",") {
				dependencyID = strings.TrimSpace(dependencyID)
				if dependencyID == "" {
					continue
				}
				if _, err := task.ParseTaskID(dependencyID); err != nil {
					return nil, fmt.Errorf("%w: dependency ID", ErrInvalidDocument)
				}
				dependsOn = append(dependsOn, dependencyID)
			}
		}
		dependencies = append(dependencies, workflow.Dependency{TaskID: cells[0], DependsOn: dependsOn})
	}
	return dependencies, nil
}

func tableCells(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil, false
	}
	raw := strings.Split(strings.Trim(line, "|"), "|")
	cells := make([]string, len(raw))
	for index, cell := range raw {
		cells[index] = strings.TrimSpace(cell)
	}
	return cells, true
}

func escapedTableCells(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil, false
	}
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	cells := make([]string, 0, 4)
	var current strings.Builder
	escaped := false
	for _, character := range line {
		switch {
		case escaped && character == '|':
			current.WriteRune(character)
			escaped = false
		case escaped:
			current.WriteRune('\\')
			current.WriteRune(character)
			escaped = false
		case character == '\\':
			escaped = true
		case character == '|':
			cells = append(cells, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteRune(character)
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	cells = append(cells, strings.TrimSpace(current.String()))
	return cells, true
}
