package process

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/interaction"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/organization"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

const CompanyActivityVersion = "workcairn-company-activity.v1"

const (
	employeeStatusStandby       = "待機中"
	employeeStatusWorking       = "作業中"
	employeeStatusReviewing     = "レビュー中"
	employeeStatusRevising      = "修正中"
	employeeStatusCompleted     = "完了"
	employeeStatusWithCEO       = "社長と相談中"
	companyLiaisonRole          = "Product Manager"
	feedLabelWorkStarted        = "作成を開始"
	feedLabelSubmittedForReview = "成果物をレビューへ提出"
	feedLabelRequestChanges     = "修正を依頼"
	feedLabelRevisionSubmitted  = "修正版を提出"
	feedLabelReviewCompleted    = "レビュー完了"
)

type EmployeeActivity struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Role             string `json:"role"`
	Department       string `json:"department"`
	DisplayStatus    string `json:"display_status"`
	CurrentWorkTitle string `json:"current_work_title,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	IsLiaison        bool   `json:"is_liaison,omitempty"`
}

type CompanyFeedEvent struct {
	At         string `json:"at"`
	SessionID  string `json:"session_id,omitempty"`
	ActorID    string `json:"actor_id,omitempty"`
	ActorName  string `json:"actor_name"`
	TargetID   string `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
	Label      string `json:"label"`
	Directed   bool   `json:"directed,omitempty"`
}

type CompanyActivity struct {
	Version   string             `json:"version"`
	Employees []EmployeeActivity `json:"employees"`
	Feed      []CompanyFeedEvent `json:"feed,omitempty"`
}

type employeeActivityCandidate struct {
	priority         int
	displayStatus    string
	currentWorkTitle string
	sessionID        string
}

// InspectCompanyActivity projects user-facing employee activity from existing
// Organization inventory, Interaction sessions, Work Reports, and Command
// Ledger evidence. It never infers business state beyond canonical records.
func InspectCompanyActivity(ctx context.Context, vaultRoot string) (CompanyActivity, error) {
	if ctx == nil {
		return CompanyActivity{}, fmt.Errorf("inspect company activity: context is required")
	}
	inspection, err := InspectOrganization(ctx, vaultRoot)
	if err != nil {
		return CompanyActivity{}, err
	}
	sessions, err := InspectInteractions(ctx, vaultRoot)
	if err != nil {
		return CompanyActivity{}, err
	}
	employeesByID := employeeDirectory(inspection.Inventory.Employees)
	employeesByRole := employeeDirectoryByRole(inspection.Inventory.Employees)
	candidates := make(map[string]employeeActivityCandidate)
	feed := make([]CompanyFeedEvent, 0)
	ceoAttentionRequired := false
	for index := len(sessions) - 1; index >= 0; index-- {
		session := sessions[index]
		if sessionNeedsCEOAttention(session.State) {
			ceoAttentionRequired = true
		}
		report, reportErr := InspectWorkReport(ctx, vaultRoot, session.SessionID)
		proofByTask := map[string]TaskProof{}
		if reportErr == nil {
			for _, taskProof := range report.Proof.Tasks {
				proofByTask[taskProof.TaskID] = taskProof
				considerTaskProof(candidates, session.SessionID, taskProof)
			}
		}
		appendSessionFeed(&feed, session, employeesByID, employeesByRole, proofByTask)
	}
	sort.Slice(feed, func(left, right int) bool { return feed[left].At < feed[right].At })
	if len(feed) > 40 {
		feed = feed[len(feed)-40:]
	}
	result := CompanyActivity{Version: CompanyActivityVersion, Employees: make([]EmployeeActivity, 0, len(inspection.Inventory.Employees)), Feed: feed}
	for _, employee := range inspection.Inventory.Employees {
		activity := EmployeeActivity{
			ID: employee.ID, Name: employee.Name, Role: employee.Role, Department: employee.Department,
			DisplayStatus: employeeStatusStandby, IsLiaison: employee.Role == companyLiaisonRole,
		}
		if employee.Role == companyLiaisonRole && ceoAttentionRequired {
			activity.DisplayStatus = employeeStatusWithCEO
		} else if candidate, ok := candidates[employee.ID]; ok {
			activity.DisplayStatus = candidate.displayStatus
			activity.CurrentWorkTitle = candidate.currentWorkTitle
			activity.SessionID = candidate.sessionID
		} else if title := strings.TrimSpace(employee.CurrentTask); title != "" {
			activity.CurrentWorkTitle = title
			if status := strings.TrimSpace(employee.Status); status != "" {
				activity.DisplayStatus = mapOrganizationStatus(status)
			}
		} else if status := strings.TrimSpace(employee.Status); status != "" {
			activity.DisplayStatus = mapOrganizationStatus(status)
		}
		result.Employees = append(result.Employees, activity)
	}
	return result, nil
}

func employeeDirectory(employees []organization.Identity) map[string]organization.Identity {
	directory := make(map[string]organization.Identity, len(employees))
	for _, employee := range employees {
		directory[strings.TrimSpace(employee.ID)] = employee
	}
	return directory
}

func employeeDirectoryByRole(employees []organization.Identity) map[string]organization.Identity {
	directory := make(map[string]organization.Identity, len(employees))
	for _, employee := range employees {
		directory[strings.TrimSpace(employee.Role)] = employee
	}
	return directory
}

func employeeDisplayName(employee organization.Identity) string {
	name := strings.TrimSpace(employee.Name)
	if name != "" {
		return name
	}
	return strings.TrimSpace(employee.ID)
}

func sessionNeedsCEOAttention(state interaction.State) bool {
	switch state {
	case interaction.StatePlanGenerationApprovalRequired, interaction.StateClarificationRequired,
		interaction.StatePlanApprovalRequired, interaction.StateReadyToExecute:
		return true
	default:
		return false
	}
}

func appendSessionFeed(feed *[]CompanyFeedEvent, session interaction.Record, employeesByID, employeesByRole map[string]organization.Identity, proofByTask map[string]TaskProof) {
	for turnIndex, turn := range session.Turns {
		at := turn.At.UTC().Format("2006-01-02T15:04:05Z07:00")
		if turn.Kind == interaction.TurnPlanApplied {
			if planTurn := latestPlanBefore(session.Turns, turnIndex); planTurn != nil && planTurn.Plan != nil {
				for _, proposed := range planTurn.Plan.ProposedTasks {
					assignee, ok := employeesByRole[strings.TrimSpace(proposed.RequiredRole)]
					if !ok {
						continue
					}
					*feed = append(*feed, CompanyFeedEvent{
						At: at, SessionID: session.SessionID,
						ActorID: assignee.ID, ActorName: employeeDisplayName(assignee),
						Label: strings.TrimSpace(proposed.Title) + "の作成を開始",
					})
				}
			}
		}
		if turn.Workflow == nil {
			continue
		}
		reviewer := employeesByID[strings.TrimSpace(turn.Workflow.ReviewerID)]
		for _, workflowTask := range turn.Workflow.Tasks {
			proof := proofByTask[workflowTask.TaskID]
			maker := employeesByID[strings.TrimSpace(proof.MakerID)]
			if maker.ID == "" {
				continue
			}
			title := strings.TrimSpace(proof.Title)
			if title == "" {
				title = workflowTask.TaskID
			}
			switch {
			case workflowTask.ExecutionCommandID != "" && !workflowTask.TargetedRevision:
				*feed = append(*feed, CompanyFeedEvent{
					At: at, SessionID: session.SessionID,
					ActorID: maker.ID, ActorName: employeeDisplayName(maker),
					Label: title + "の" + feedLabelWorkStarted,
				})
				if reviewer.ID != "" && (proof.Deliverable.Committed || workflowTask.ReviewCommandID != "") {
					*feed = append(*feed, CompanyFeedEvent{
						At: at, SessionID: session.SessionID,
						ActorID: maker.ID, ActorName: employeeDisplayName(maker),
						TargetID: reviewer.ID, TargetName: employeeDisplayName(reviewer),
						Label:    feedLabelSubmittedForReview,
						Directed: true,
					})
				}
			case workflowTask.TargetedRevision:
				*feed = append(*feed, CompanyFeedEvent{
					At: at, SessionID: session.SessionID,
					ActorID: maker.ID, ActorName: employeeDisplayName(maker),
					Label: feedLabelRevisionSubmitted,
				})
			}
			if workflowTask.Verdict == review.VerdictRequestChanges && reviewer.ID != "" {
				*feed = append(*feed, CompanyFeedEvent{
					At: at, SessionID: session.SessionID,
					ActorID: reviewer.ID, ActorName: employeeDisplayName(reviewer),
					TargetID: maker.ID, TargetName: employeeDisplayName(maker),
					Label:    feedLabelRequestChanges,
					Directed: true,
				})
			}
			if workflowTask.Verdict == review.VerdictApprove && reviewer.ID != "" {
				*feed = append(*feed, CompanyFeedEvent{
					At: at, SessionID: session.SessionID,
					ActorID: reviewer.ID, ActorName: employeeDisplayName(reviewer),
					Label: feedLabelReviewCompleted,
				})
			}
		}
	}
}

func latestPlanBefore(turns []interaction.Turn, turnIndex int) *interaction.Turn {
	for index := turnIndex - 1; index >= 0; index-- {
		turn := turns[index]
		if turn.Kind == interaction.TurnPlanGenerated && turn.Plan != nil {
			return &turn
		}
	}
	return nil
}

func considerTaskProof(candidates map[string]employeeActivityCandidate, sessionID string, proof TaskProof) {
	title := strings.TrimSpace(proof.Title)
	if title == "" {
		title = proof.TaskID
	}
	runningExecution, runningReview, runningRevision := commandStates(proof.Commands)
	if proof.MakerID != "" {
		status, priority := makerActivity(proof, runningExecution, runningRevision)
		considerEmployee(candidates, proof.MakerID, employeeActivityCandidate{
			priority: priority, displayStatus: status, currentWorkTitle: title, sessionID: sessionID,
		})
	}
	if proof.Review.ReviewerID != "" {
		status, priority := reviewerActivity(proof, runningReview)
		considerEmployee(candidates, proof.Review.ReviewerID, employeeActivityCandidate{
			priority: priority, displayStatus: status, currentWorkTitle: title, sessionID: sessionID,
		})
	}
}

func makerActivity(proof TaskProof, runningExecution, runningRevision bool) (string, int) {
	switch {
	case runningExecution || proof.Status == task.StatusInProgress:
		return employeeStatusWorking, 50
	case runningRevision || (proof.Revision.Occurred && proof.Status != task.StatusCompleted):
		return employeeStatusRevising, 35
	case proof.Status == task.StatusCompleted && proof.Deliverable.Committed && proof.Review.Verdict == "":
		return employeeStatusWorking, 20
	case proof.Verified:
		return employeeStatusCompleted, 10
	case proof.Status == task.StatusCompleted:
		return employeeStatusCompleted, 10
	default:
		return employeeStatusStandby, 0
	}
}

func reviewerActivity(proof TaskProof, runningReview bool) (string, int) {
	switch {
	case runningReview:
		return employeeStatusReviewing, 45
	case proof.Deliverable.Committed && proof.Review.Verdict == "":
		return employeeStatusReviewing, 30
	case proof.Review.Verdict == review.VerdictRequestChanges:
		return employeeStatusReviewing, 15
	case proof.Review.Verdict == review.VerdictApprove:
		return employeeStatusCompleted, 10
	default:
		return employeeStatusStandby, 0
	}
}

func commandStates(commands []CommandProof) (runningExecution, runningReview, runningRevision bool) {
	for _, command := range commands {
		if command.State != commandledger.StateRunning {
			continue
		}
		switch {
		case strings.Contains(command.Operation, "review"):
			runningReview = true
		case strings.Contains(command.Operation, "revision"):
			runningRevision = true
		default:
			runningExecution = true
		}
	}
	return runningExecution, runningReview, runningRevision
}

func considerEmployee(candidates map[string]employeeActivityCandidate, employeeID string, candidate employeeActivityCandidate) {
	employeeID = strings.TrimSpace(employeeID)
	if employeeID == "" {
		return
	}
	current, ok := candidates[employeeID]
	if !ok || candidate.priority > current.priority {
		candidates[employeeID] = candidate
	}
}

func mapOrganizationStatus(status string) string {
	switch status {
	case "待機中", "必要時に参加":
		return employeeStatusStandby
	case "進行中":
		return employeeStatusWorking
	case "完了":
		return employeeStatusCompleted
	default:
		return status
	}
}
