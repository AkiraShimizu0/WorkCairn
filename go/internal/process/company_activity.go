package process

import (
	"context"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

const CompanyActivityVersion = "workcairn-company-activity.v1"

const (
	employeeStatusStandby   = "待機中"
	employeeStatusWorking   = "作業中"
	employeeStatusReviewing = "レビュー中"
	employeeStatusRevising  = "修正中"
	employeeStatusCompleted = "完了"
)

type EmployeeActivity struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Role             string `json:"role"`
	Department       string `json:"department"`
	DisplayStatus    string `json:"display_status"`
	CurrentWorkTitle string `json:"current_work_title,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
}

type CompanyActivity struct {
	Version   string             `json:"version"`
	Employees []EmployeeActivity `json:"employees"`
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
	candidates := make(map[string]employeeActivityCandidate)
	for index := len(sessions) - 1; index >= 0; index-- {
		session := sessions[index]
		report, reportErr := InspectWorkReport(ctx, vaultRoot, session.SessionID)
		if reportErr != nil {
			continue
		}
		for _, taskProof := range report.Proof.Tasks {
			considerTaskProof(candidates, session.SessionID, taskProof)
		}
	}
	result := CompanyActivity{Version: CompanyActivityVersion, Employees: make([]EmployeeActivity, 0, len(inspection.Inventory.Employees))}
	for _, employee := range inspection.Inventory.Employees {
		activity := EmployeeActivity{
			ID: employee.ID, Name: employee.Name, Role: employee.Role, Department: employee.Department,
			DisplayStatus: employeeStatusStandby,
		}
		if candidate, ok := candidates[employee.ID]; ok {
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
