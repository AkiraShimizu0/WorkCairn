package project

import taskdomain "github.com/AkiraShimizu0/workspace-os/go/internal/task"

var (
	ErrInvalidTaskTitle  = taskdomain.ErrInvalidTaskTitle
	ErrInvalidAssigneeID = taskdomain.ErrInvalidAssigneeID
)

// ValidateTask validates only portable task rules; employee existence is external.
func ValidateTask(projectTask Task) error {
	return taskdomain.Task{
		ID:         projectTask.ID,
		Title:      projectTask.Title,
		AssigneeID: projectTask.AssigneeID,
		Status:     projectTask.Status,
		Version:    1,
	}.Validate()
}
