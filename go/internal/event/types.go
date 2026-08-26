package event

// Type is a closed set of WorkCairn business event types.
type Type string

const (
	ProjectCreated            Type = "project.created"
	TaskCreated               Type = "task.created"
	TaskStarted               Type = "task.started"
	TaskCompleted             Type = "task.completed"
	TaskFailed                Type = "task.failed"
	TaskHeld                  Type = "task.held"
	TaskResumed               Type = "task.resumed"
	ReviewRequested           Type = "review.requested"
	ReviewCompleted           Type = "review.completed"
	RevisionCreated           Type = "revision.created"
	EmployeeCreated           Type = "employee.created"
	EmployeeRenamed           Type = "employee.renamed"
	WorkflowStarted           Type = "workflow.started"
	WorkflowCompleted         Type = "workflow.completed"
	ActionCompleted           Type = "action.completed"
	GoalCreated               Type = "goal.created"
	GoalAchieved              Type = "goal.achieved"
	GoalAbandoned             Type = "goal.abandoned"
	ResponsibilityCreated     Type = "responsibility.created"
	ResponsibilityActivated   Type = "responsibility.activated"
	ResponsibilityDeactivated Type = "responsibility.deactivated"
	ResponsibilityAssigned    Type = "responsibility.assigned"
	ResponsibilityUnassigned  Type = "responsibility.unassigned"
	// RoutineCreated/Activated/Deactivated are the only Routine lifecycle
	// Events (ADR-0063) -- occurrence-level facts (a Routine firing,
	// generating a Plan, or failing) are already observable via the
	// existing Schedule Record and Command Ledger, so no
	// routine.triggered/plan_generated/failed Event was added.
	RoutineCreated     Type = "routine.created"
	RoutineActivated   Type = "routine.activated"
	RoutineDeactivated Type = "routine.deactivated"
)

var supportedTypes = map[Type]struct{}{
	ProjectCreated:            {},
	TaskCreated:               {},
	TaskStarted:               {},
	TaskCompleted:             {},
	TaskFailed:                {},
	TaskHeld:                  {},
	TaskResumed:               {},
	ReviewRequested:           {},
	ReviewCompleted:           {},
	RevisionCreated:           {},
	EmployeeCreated:           {},
	EmployeeRenamed:           {},
	WorkflowStarted:           {},
	WorkflowCompleted:         {},
	ActionCompleted:           {},
	GoalCreated:               {},
	GoalAchieved:              {},
	GoalAbandoned:             {},
	ResponsibilityCreated:     {},
	ResponsibilityActivated:   {},
	ResponsibilityDeactivated: {},
	ResponsibilityAssigned:    {},
	ResponsibilityUnassigned:  {},
	RoutineCreated:            {},
	RoutineActivated:          {},
	RoutineDeactivated:        {},
}

func (eventType Type) Valid() bool {
	_, exists := supportedTypes[eventType]
	return exists
}

// Supported lets Adapter contracts validate a stored Event type without
// exposing or duplicating the closed-set registry.
func Supported(eventType Type) bool { return eventType.Valid() }
