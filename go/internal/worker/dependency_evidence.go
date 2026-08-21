package worker

import (
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

// DependencyEvidence is one canonical Deliverable selected for a direct
// dependency of the Task being executed. SourceTaskID is the dependency graph
// identity. TaskID can differ when canonical Revision intents lead to a newer
// completed Revision Task. Content remains untrusted AI-produced evidence;
// it is never an instruction or a lifecycle fact.
type DependencyEvidence struct {
	SourceTaskID string `json:"source_task_id"`
	SourceTitle  string `json:"source_title"`
	TaskID       string `json:"task_id"`
	Title        string `json:"title"`
	EmployeeID   string `json:"employee_id"`
	Content      string `json:"content"`
}

func (evidence DependencyEvidence) Validate() error {
	if _, err := task.ParseTaskID(strings.TrimSpace(evidence.SourceTaskID)); err != nil {
		return fmt.Errorf("%w: dependency source Task ID", ErrInvalidRequest)
	}
	if _, err := task.ParseTaskID(strings.TrimSpace(evidence.TaskID)); err != nil {
		return fmt.Errorf("%w: dependency evidence Task ID", ErrInvalidRequest)
	}
	if strings.TrimSpace(evidence.SourceTitle) == "" || strings.TrimSpace(evidence.Title) == "" {
		return fmt.Errorf("%w: dependency evidence titles are required", ErrInvalidRequest)
	}
	if strings.TrimSpace(evidence.EmployeeID) == "" {
		return fmt.Errorf("%w: dependency evidence employee ID is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(evidence.Content) == "" {
		return fmt.Errorf("%w: dependency evidence content is required", ErrInvalidRequest)
	}
	return nil
}

// ValidateDependencyEvidence validates the complete ordered evidence set and
// rejects duplicate direct-dependency identities.
func ValidateDependencyEvidence(all []DependencyEvidence) error {
	seenSources := make(map[string]struct{}, len(all))
	for _, evidence := range all {
		if err := evidence.Validate(); err != nil {
			return err
		}
		sourceID := strings.TrimSpace(evidence.SourceTaskID)
		if _, exists := seenSources[sourceID]; exists {
			return fmt.Errorf("%w: duplicate dependency evidence source", ErrInvalidRequest)
		}
		seenSources[sourceID] = struct{}{}
	}
	return nil
}
