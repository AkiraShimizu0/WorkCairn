package workflow

import (
	"errors"
	"fmt"
)

const (
	StatusUnstarted = "未着手"
	StatusCompleted = "完了"
)

// State is the portable workflow readiness state.
type State string

const (
	StateReady     State = "ready"
	StateBlocked   State = "blocked"
	StateWaiting   State = "waiting"
	StateCompleted State = "completed"
)

var (
	ErrUnknownDependency = errors.New("unknown dependency")
	ErrCyclicDependency  = errors.New("cyclic dependency")
	ErrDuplicateTaskID   = errors.New("duplicate task ID")
)

// Task is the minimum task data required by the Go workflow core.
type Task struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	AssigneeID *string `json:"assignee_id"`
	Status     string  `json:"status"`
}

// ReadinessResult is independent of Python, Obsidian, and any AI SDK.
type ReadinessResult struct {
	TaskID          string   `json:"task_id"`
	Title           string   `json:"title"`
	AssigneeID      *string  `json:"assignee_id"`
	Dependencies    []string `json:"dependencies"`
	BlockedBy       []string `json:"blocked_by"`
	Ready           bool     `json:"ready"`
	State           State    `json:"state"`
	Reason          string   `json:"reason"`
	BlockingReasons []string `json:"blocking_reasons"`
	NextAction      string   `json:"next_action"`
}

// ValidateDependencies checks existence, duplicate IDs, and cycles.
func ValidateDependencies(
	tasks []Task,
	dependencies []Dependency,
) (map[string][]string, error) {
	taskIDs := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if _, exists := taskIDs[task.ID]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateTaskID, task.ID)
		}
		taskIDs[task.ID] = struct{}{}
	}

	graph := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		graph[task.ID] = []string{}
	}
	seenDependencyRows := make(map[string]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		if _, exists := taskIDs[dependency.TaskID]; !exists {
			return nil, fmt.Errorf(
				"%w: dependency metadata task %s",
				ErrUnknownDependency,
				dependency.TaskID,
			)
		}
		if _, exists := seenDependencyRows[dependency.TaskID]; exists {
			return nil, fmt.Errorf(
				"duplicate dependency metadata task: %s",
				dependency.TaskID,
			)
		}
		seenDependencyRows[dependency.TaskID] = struct{}{}
		graph[dependency.TaskID] = append([]string(nil), dependency.DependsOn...)
		for _, dependencyID := range dependency.DependsOn {
			if _, exists := taskIDs[dependencyID]; !exists {
				return nil, fmt.Errorf("%w: %s", ErrUnknownDependency, dependencyID)
			}
		}
	}

	states := make(map[string]uint8, len(tasks))
	var visit func(string) error
	visit = func(taskID string) error {
		switch states[taskID] {
		case 1:
			return fmt.Errorf("%w: %s", ErrCyclicDependency, taskID)
		case 2:
			return nil
		}
		states[taskID] = 1
		for _, dependencyID := range graph[taskID] {
			if err := visit(dependencyID); err != nil {
				return err
			}
		}
		states[taskID] = 2
		return nil
	}

	for _, task := range tasks {
		if err := visit(task.ID); err != nil {
			return nil, err
		}
	}
	return graph, nil
}

// EvaluateReadiness selects the same first unstarted task as WorkflowEngine.
func EvaluateReadiness(
	tasks []Task,
	dependencies []Dependency,
	existingEmployees map[string]bool,
) (ReadinessResult, error) {
	graph, err := ValidateDependencies(tasks, dependencies)
	if err != nil {
		return ReadinessResult{}, err
	}

	allCompleted := len(tasks) > 0
	var pendingTask *Task
	for index := range tasks {
		task := &tasks[index]
		if task.Status != StatusCompleted {
			allCompleted = false
		}
		if pendingTask == nil && task.Status == StatusUnstarted {
			pendingTask = task
		}
	}
	if allCompleted {
		return emptyResult(StateCompleted, "all_tasks_completed", "none"), nil
	}
	if pendingTask == nil {
		return emptyResult(StateWaiting, "no_unstarted_tasks", "wait"), nil
	}

	dependencyIDs := append([]string{}, graph[pendingTask.ID]...)
	statusByID := make(map[string]string, len(tasks))
	for _, task := range tasks {
		statusByID[task.ID] = task.Status
	}
	blockedBy := make([]string, 0)
	for _, dependencyID := range dependencyIDs {
		if statusByID[dependencyID] != StatusCompleted {
			blockedBy = append(blockedBy, dependencyID)
		}
	}

	blockingReasons := make([]string, 0)
	if pendingTask.AssigneeID == nil {
		blockingReasons = append(blockingReasons, "assignee_missing")
	} else if !existingEmployees[*pendingTask.AssigneeID] {
		blockingReasons = append(blockingReasons, "assignee_not_found")
	}
	if len(blockedBy) > 0 {
		blockingReasons = append(blockingReasons, "dependencies_incomplete")
	}

	if len(blockingReasons) > 0 {
		return taskResult(
			*pendingTask,
			dependencyIDs,
			blockedBy,
			false,
			StateBlocked,
			blockingReasons[0],
			blockingReasons,
			"resolve_blockers",
		), nil
	}

	return taskResult(
		*pendingTask,
		dependencyIDs,
		[]string{},
		true,
		StateReady,
		"ready",
		[]string{},
		"workflow_execute",
	), nil
}

func taskResult(
	task Task,
	dependencies []string,
	blockedBy []string,
	ready bool,
	state State,
	reason string,
	blockingReasons []string,
	nextAction string,
) ReadinessResult {
	return ReadinessResult{
		TaskID:          task.ID,
		Title:           task.Title,
		AssigneeID:      task.AssigneeID,
		Dependencies:    dependencies,
		BlockedBy:       blockedBy,
		Ready:           ready,
		State:           state,
		Reason:          reason,
		BlockingReasons: blockingReasons,
		NextAction:      nextAction,
	}
}

func emptyResult(state State, reason, nextAction string) ReadinessResult {
	return ReadinessResult{
		Dependencies:    []string{},
		BlockedBy:       []string{},
		Ready:           false,
		State:           state,
		Reason:          reason,
		BlockingReasons: []string{},
		NextAction:      nextAction,
	}
}
