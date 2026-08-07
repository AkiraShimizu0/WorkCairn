package project

// Task is the storage-independent project task domain value.
type Task struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	AssigneeID *string `json:"assignee_id"`
	Status     Status  `json:"status"`
}
