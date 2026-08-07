package event

// Type is a closed set of Workspace OS business event types.
type Type string

const (
	ProjectCreated    Type = "project.created"
	TaskCreated       Type = "task.created"
	TaskStarted       Type = "task.started"
	TaskCompleted     Type = "task.completed"
	TaskFailed        Type = "task.failed"
	TaskHeld          Type = "task.held"
	ReviewRequested   Type = "review.requested"
	ReviewCompleted   Type = "review.completed"
	RevisionCreated   Type = "revision.created"
	EmployeeCreated   Type = "employee.created"
	EmployeeRenamed   Type = "employee.renamed"
	WorkflowStarted   Type = "workflow.started"
	WorkflowCompleted Type = "workflow.completed"
)

var supportedTypes = map[Type]struct{}{
	ProjectCreated:    {},
	TaskCreated:       {},
	TaskStarted:       {},
	TaskCompleted:     {},
	TaskFailed:        {},
	TaskHeld:          {},
	ReviewRequested:   {},
	ReviewCompleted:   {},
	RevisionCreated:   {},
	EmployeeCreated:   {},
	EmployeeRenamed:   {},
	WorkflowStarted:   {},
	WorkflowCompleted: {},
}

func (eventType Type) Valid() bool {
	_, exists := supportedTypes[eventType]
	return exists
}
