package project

import (
	"fmt"
	"regexp"
	"strings"
)

var proposalIDPattern = regexp.MustCompile(`^PROPOSED-\d+$`)

type TaskDependency struct {
	TaskID     string   `json:"task_id"`
	ProposalID string   `json:"proposal_id"`
	DependsOn  []string `json:"depends_on"`
	Rationale  string   `json:"rationale"`
}

func ValidateTaskDependencies(rows []TaskDependency, knownTaskIDs map[string]bool) error {
	if len(rows) == 0 {
		return fmt.Errorf("Task dependencies are required")
	}
	seenTasks, seenProposals := map[string]bool{}, map[string]bool{}
	graph := make(map[string][]string, len(rows))
	for _, row := range rows {
		if !knownTaskIDs[row.TaskID] || !proposalIDPattern.MatchString(row.ProposalID) || strings.TrimSpace(row.Rationale) == "" {
			return fmt.Errorf("invalid Task dependency row")
		}
		if seenTasks[row.TaskID] || seenProposals[row.ProposalID] {
			return fmt.Errorf("duplicate Task or Proposed ID")
		}
		seenTasks[row.TaskID], seenProposals[row.ProposalID] = true, true
		graph[row.TaskID] = append([]string(nil), row.DependsOn...)
	}
	for taskID, dependencies := range graph {
		seen := map[string]bool{}
		for _, dependencyID := range dependencies {
			if dependencyID == taskID || !knownTaskIDs[dependencyID] || seen[dependencyID] {
				return fmt.Errorf("invalid dependency ID")
			}
			seen[dependencyID] = true
		}
	}
	states := map[string]uint8{}
	var visit func(string) error
	visit = func(taskID string) error {
		if states[taskID] == 1 {
			return fmt.Errorf("cyclic Task dependencies")
		}
		if states[taskID] == 2 {
			return nil
		}
		states[taskID] = 1
		for _, dependencyID := range graph[taskID] {
			if _, inPlan := graph[dependencyID]; inPlan {
				if err := visit(dependencyID); err != nil {
					return err
				}
			}
		}
		states[taskID] = 2
		return nil
	}
	for taskID := range graph {
		if err := visit(taskID); err != nil {
			return err
		}
	}
	return nil
}
