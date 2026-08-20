// Package httpapi defines the versioned HTTP edge for WorkCairn commands.
// It depends on application process composition, never the other way around.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
)

const (
	ContractVersion            = "workspace-command.v1"
	InteractionContractVersion = "workspace-interaction.v1"
	ProviderStatusVersion      = "workcairn-provider-status.v1"
	AutomaticInteractionModel  = "workcairn-auto"
)

const WorkspaceStatusVersion = "workcairn-workspace-status.v1"

type WorkspaceStatus struct {
	Version             string                      `json:"version"`
	StorageKind         string                      `json:"storage_kind"`
	ObsidianCompatible  bool                        `json:"obsidian_compatible"`
	LayoutReady         bool                        `json:"layout_ready"`
	OrganizationReady   bool                        `json:"organization_ready"`
	StarterOrganization []StarterOrganizationMember `json:"starter_organization"`
	MissingRoles        []string                    `json:"missing_roles"`
}

// StarterOrganizationMember is the redacted setup projection. Persistent IDs,
// logical model routes, file paths, and Provider configuration stay server-side.
type StarterOrganizationMember struct {
	Name       string `json:"name"`
	Department string `json:"department"`
	Role       string `json:"role"`
}

var (
	ErrInvalidCommand     = errors.New("invalid HTTP command")
	ErrUnsupportedCommand = errors.New("unsupported HTTP command")
)

// publicBetaCommandOperations is the complete side-effect surface exposed by
// the general workcairn-daemon. Internal Process composition, the operator CLI,
// Scheduler, and Recovery keep their existing capabilities; only the public
// HTTP product edge is narrowed here. Unknown and operator-only operations are
// denied before they can reach an Executor.
var publicBetaCommandOperations = map[string]struct{}{
	"workspace.setup":           {},
	"interaction.start":         {},
	"interaction.plan.generate": {},
	"interaction.answer":        {},
	// interaction.plan.apply and interaction.workflow.execute are kept
	// allow-listed for operator/Recovery parity (ADR-0049 section 16) even
	// though the normal product path now uses only
	// interaction.plan.approve_and_execute below -- Record.Next() no longer
	// points a normal session at either standalone operation, but a human
	// resuming a session stuck mid-chain after a daemon crash still needs
	// interaction.workflow.execute to be directly callable.
	"interaction.plan.apply":               {},
	"interaction.workflow.execute":         {},
	"interaction.plan.approve_and_execute": {},
	// interaction.archive/interaction.unarchive only toggle the Session's
	// active-request-list visibility (a new TurnArchived/TurnUnarchived
	// Turn) -- they never touch Project, Task, Review, Revision, or
	// Deliverable evidence, so they are safe to allow-list identically to
	// every other interaction.* write.
	"interaction.archive":   {},
	"interaction.unarchive": {},
	// interaction.workflow.recover_revision is the CEO's single explicit
	// action after a Revision Limit stop (ADR-0051 Revision Guard;
	// Revision Limit Recovery). Record.Next() only ever offers it from
	// StateWorkflowAttentionRequired when the recorded Failure is
	// specifically REVISION_LIMIT_REACHED and a stalled Task genuinely
	// exists -- never a caller-visible retry/parallel-style choice.
	"interaction.workflow.recover_revision": {},
}

func publicBetaCommandAllowed(operation string) bool {
	_, allowed := publicBetaCommandOperations[operation]
	return allowed
}

type Command struct {
	Version   string          `json:"version"`
	CommandID string          `json:"command_id"`
	Operation string          `json:"operation"`
	Approved  bool            `json:"approved"`
	Payload   json.RawMessage `json:"payload"`
}

func (command Command) Validate() error {
	if command.Version != ContractVersion || commandledger.ValidateCommandID(command.CommandID) != nil ||
		strings.TrimSpace(command.Operation) == "" || command.Operation != strings.TrimSpace(command.Operation) ||
		strings.ContainsAny(command.Operation, "\r\n") || len(command.Payload) == 0 || !json.Valid(command.Payload) {
		return ErrInvalidCommand
	}
	return nil
}

type CommandError struct {
	Code             string `json:"code"`
	Stage            string `json:"stage,omitempty"`
	RecoveryRequired bool   `json:"recovery_required,omitempty"`
	// Details is the additive, optional full failure.Envelope behind
	// Code/Stage/RecoveryRequired, present whenever the failing Command
	// has migrated to Envelope propagation. Clients keep reading
	// Code/Stage/RecoveryRequired as the source of truth; Details is
	// enrichment for a richer UI projection, not a replacement.
	Details *failure.Envelope `json:"details,omitempty"`
}

type Response struct {
	Version   string          `json:"version"`
	CommandID string          `json:"command_id,omitempty"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *CommandError   `json:"error,omitempty"`
}

type InteractionPlanRequest struct {
	Version     string    `json:"version"`
	SessionID   string    `json:"session_id"`
	Request     string    `json:"request"`
	Model       string    `json:"model"`
	CurrentTime time.Time `json:"current_time"`
}

// ProviderStatus is a redacted Runtime-edge inspection. Configured means the
// Adapter can be constructed; it does not perform a network connection check.
type ProviderStatus struct {
	Version       string   `json:"version"`
	Provider      string   `json:"provider"`
	Configured    bool     `json:"configured"`
	SelectionMode string   `json:"selection_mode"`
	Missing       []string `json:"missing"`
	Invalid       []string `json:"invalid"`
}

type InteractionWorkflowPlanRequest struct {
	Version         string             `json:"version"`
	SessionID       string             `json:"session_id"`
	ExpectedVersion uint64             `json:"expected_version"`
	ReviewerID      string             `json:"reviewer_id"`
	CurrentTime     time.Time          `json:"current_time"`
	MaxTasks        int                `json:"max_tasks"`
	Autonomy        *autonomy.Contract `json:"autonomy_contract,omitempty"`
}

type InteractionActionPlanRequest struct {
	Version         string    `json:"version"`
	SessionID       string    `json:"session_id"`
	ExpectedVersion uint64    `json:"expected_version"`
	TaskID          string    `json:"task_id"`
	TargetID        string    `json:"target_id"`
	CurrentTime     time.Time `json:"current_time"`
	CommandID       string    `json:"command_id"`
}

func (request InteractionPlanRequest) Validate() error {
	if request.Version != InteractionContractVersion || interaction.ValidateSessionID(request.SessionID) != nil ||
		strings.TrimSpace(request.Request) == "" || len(request.Request) > 32<<10 ||
		strings.TrimSpace(request.Model) == "" || strings.ContainsAny(request.Model, "\r\n") || request.CurrentTime.IsZero() {
		return ErrInvalidCommand
	}
	return nil
}

func (request InteractionPlanRequest) withDefaults() InteractionPlanRequest {
	if strings.TrimSpace(request.Model) == "" {
		request.Model = AutomaticInteractionModel
	}
	return request
}

func (request InteractionWorkflowPlanRequest) Validate() error {
	if request.Version != InteractionContractVersion || interaction.ValidateSessionID(request.SessionID) != nil || request.ExpectedVersion == 0 ||
		(request.ReviewerID != "" && (request.ReviewerID != strings.TrimSpace(request.ReviewerID) || strings.ContainsAny(request.ReviewerID, "\r\n"))) ||
		request.CurrentTime.IsZero() || request.MaxTasks <= 0 || request.MaxTasks > 100 {
		return ErrInvalidCommand
	}
	if request.Autonomy != nil && (request.Autonomy.Validate() != nil || request.Autonomy.ExecutionLimit != request.MaxTasks) {
		return ErrInvalidCommand
	}
	return nil
}

func (request InteractionActionPlanRequest) Validate() error {
	if request.Version != InteractionContractVersion || interaction.ValidateSessionID(request.SessionID) != nil || request.ExpectedVersion == 0 ||
		strings.TrimSpace(request.TaskID) == "" || request.TaskID != strings.TrimSpace(request.TaskID) ||
		strings.TrimSpace(request.TargetID) == "" || request.TargetID != strings.TrimSpace(request.TargetID) ||
		request.CurrentTime.IsZero() || commandledger.ValidateCommandID(request.CommandID) != nil {
		return ErrInvalidCommand
	}
	return nil
}

func decodePayload(content json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: payload", ErrInvalidCommand)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing payload", ErrInvalidCommand)
	}
	return nil
}
