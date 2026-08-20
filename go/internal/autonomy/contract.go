// Package autonomy defines the user-visible delegation boundary for one
// WorkCairn Workflow. It describes scope; existing approval, Workflow,
// Command Ledger, and Adapter services continue to enforce effects.
package autonomy

import (
	"errors"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = "workcairn-autonomy.v1"

// DefaultMaxParallelTasks is the Go-decided ceiling on how many
// dependency-ready Tasks a Workflow may dispatch at once (ADR-0051). It is
// conservative on purpose: Public Beta runs one Provider connection per
// Mac, and 3 concurrent Provider calls is enough to make the CEO's example
// shape (independent research/analysis branches) visibly parallel without
// saturating a single personal machine's one Provider key. This is not
// caller-configurable input -- like every other field on this fixed-floor
// Contract, Go decides it.
const DefaultMaxParallelTasks = 3

// MaxParallelTasksCeiling bounds MaxParallelTasks even if a future caller
// path raises it above DefaultMaxParallelTasks -- it must never exceed a
// small, defensible number regardless of how the value is sourced.
const MaxParallelTasksCeiling = 10

// DefaultMaxRevisionCount is the Go-decided ceiling on how many times one
// Task's own Request Changes -> Revision cycle may repeat before Reviewed
// Workflow execution stops creating further Revision Tasks for it (Revision
// Guard, ADR-0051). It exists because parallel dispatch means several
// branches can each independently churn through Revision cycles at once,
// multiplying Provider calls in a way a single sequential Workflow never
// could -- two revisions (three attempts total: the original plus two
// Request Changes rounds) is enough to let a genuinely fixable Deliverable
// converge without letting one stuck branch consume an unbounded share of
// the Workflow's Provider budget.
const DefaultMaxRevisionCount = 2

// MaxRevisionCountCeiling bounds MaxRevisionCount even if a future caller
// path raises it above DefaultMaxRevisionCount.
const MaxRevisionCountCeiling = 5

// DefaultMaxProviderCalls is the Go-decided ceiling on actual Provider
// invocations (Task execution + Review, summed across every branch) one
// Reviewed Workflow execution may make (BudgetGuard v1, ADR-0054) --
// distinct from and layered on top of the existing structural LoopGuards
// above (MaxParallelTasks, MaxRevisionCount): those bound *how the work is
// shaped*, this bounds *how much Provider usage that shape is allowed to
// consume* even while every branch is genuinely converging. Measured
// against the existing Leverage Engine happy path (4 Tasks -- 3 parallel
// branches plus Synthesis -- each Approved on first Review = 8 calls) and
// its own worst case still inside today's Revision Guard (4 Tasks x up to
// 3 attempts each x 2 calls/attempt = 24 calls), this default leaves
// generous headroom for legitimate multi-branch, multi-revision work while
// still bounding a genuinely runaway scope to a finite number.
const DefaultMaxProviderCalls = 60

// MaxProviderCallsCeiling bounds MaxProviderCalls even if a future caller
// path raises it above DefaultMaxProviderCalls.
const MaxProviderCallsCeiling = 300

// DefaultMaxRuntime is the Go-decided wall-clock ceiling for one Reviewed
// Workflow execution (BudgetGuard v1, ADR-0054). Sized against the existing
// per-request Provider timeout (runtime.DefaultProviderRequestTimeout, 5
// minutes, ADR-0045): even several sequential rounds of concurrent
// dispatch at that request timeout's worst case comfortably fit inside 30
// minutes, while a genuinely stuck scope is still guaranteed to stop
// within a bounded, human-reasonable window rather than running
// indefinitely.
const DefaultMaxRuntime = 30 * time.Minute

// MaxRuntimeCeiling bounds MaxRuntime even if a future caller path raises
// it above DefaultMaxRuntime.
const MaxRuntimeCeiling = 2 * time.Hour

var ErrInvalidContract = errors.New("invalid Autonomy Contract")

type Permission string

const (
	PermissionDelegated       Permission = "delegated"
	PermissionRequired        Permission = "required"
	PermissionSeparateApprove Permission = "separate_approval"
	PermissionForbidden       Permission = "forbidden"
)

// Contract is intentionally narrower than a general policy engine. Public
// Beta keeps a fixed safety floor while binding the approved team, models, and
// execution limit to the Workflow plan digest.
type Contract struct {
	SchemaVersion      string     `json:"schema_version"`
	TaskExecution      Permission `json:"task_execution"`
	Review             Permission `json:"review"`
	Revision           Permission `json:"revision"`
	ExternalPublish    Permission `json:"external_publish"`
	Spending           Permission `json:"spending"`
	AllowedEmployeeIDs []string   `json:"allowed_employee_ids"`
	AllowedModels      []string   `json:"allowed_models"`
	ExecutionLimit     int        `json:"execution_limit"`
	// MaxParallelTasks bounds how many dependency-ready Tasks a Workflow may
	// dispatch at once (ADR-0051, additive). Like every other field on this
	// Contract it is Go-decided, never caller input: NewStandard always sets
	// it to DefaultMaxParallelTasks.
	MaxParallelTasks int `json:"max_parallel_tasks,omitempty"`
	// MaxRevisionCount bounds how many Request Changes -> Revision cycles one
	// Task's own branch may repeat before Reviewed Workflow execution stops
	// creating further Revision Tasks for it (ADR-0051 Revision Guard,
	// additive). Go-decided like MaxParallelTasks; NewStandard always sets it
	// to DefaultMaxRevisionCount.
	MaxRevisionCount int `json:"max_revision_count,omitempty"`
	// MaxProviderCalls bounds actual Provider invocations for one Reviewed
	// Workflow execution (BudgetGuard v1, additive). Go-decided; NewStandard
	// always sets it to DefaultMaxProviderCalls. Distinct responsibility from
	// MaxParallelTasks/MaxRevisionCount above -- see those fields' own
	// constants' doc comments for the LoopGuard/BudgetGuard boundary.
	MaxProviderCalls int `json:"max_provider_calls,omitempty"`
	// MaxRuntime bounds the wall-clock duration of one Reviewed Workflow
	// execution (BudgetGuard v1, additive). Go-decided; NewStandard always
	// sets it to DefaultMaxRuntime.
	MaxRuntime time.Duration `json:"max_runtime,omitempty"`
}

func NewStandard(employeeIDs, models []string, executionLimit int) (Contract, error) {
	contract := Contract{
		SchemaVersion: SchemaVersion, TaskExecution: PermissionDelegated, Review: PermissionRequired,
		Revision: PermissionDelegated, ExternalPublish: PermissionSeparateApprove, Spending: PermissionForbidden,
		AllowedEmployeeIDs: canonical(employeeIDs), AllowedModels: canonical(models), ExecutionLimit: executionLimit,
		MaxParallelTasks: DefaultMaxParallelTasks, MaxRevisionCount: DefaultMaxRevisionCount,
		MaxProviderCalls: DefaultMaxProviderCalls, MaxRuntime: DefaultMaxRuntime,
	}
	return contract, contract.Validate()
}

func (contract Contract) Validate() error {
	if contract.SchemaVersion != SchemaVersion || contract.TaskExecution != PermissionDelegated ||
		contract.Review != PermissionRequired || contract.Revision != PermissionDelegated ||
		contract.ExternalPublish != PermissionSeparateApprove || contract.Spending != PermissionForbidden ||
		contract.ExecutionLimit < 1 || contract.ExecutionLimit > 100 ||
		// 0 is accepted as "unset" (a Contract persisted before this field
		// existed, or decoded from a pre-ADR-0051 record) and treated as
		// DefaultMaxParallelTasks by EffectiveMaxParallelTasks -- only a
		// negative or over-ceiling value is actually invalid.
		contract.MaxParallelTasks < 0 || contract.MaxParallelTasks > MaxParallelTasksCeiling ||
		// Same "0 is unset/legacy" backward-compatible rule as
		// MaxParallelTasks: a Contract persisted before MaxRevisionCount
		// existed decodes with the field absent (Go zero value 0), which
		// EffectiveMaxRevisionCount treats as DefaultMaxRevisionCount.
		contract.MaxRevisionCount < 0 || contract.MaxRevisionCount > MaxRevisionCountCeiling ||
		// Same "0 is unset/legacy" rule for the BudgetGuard v1 fields: a
		// Contract persisted before these fields existed decodes with them
		// absent (Go zero value), which EffectiveMaxProviderCalls/
		// EffectiveMaxRuntime treat as their own Default.
		contract.MaxProviderCalls < 0 || contract.MaxProviderCalls > MaxProviderCallsCeiling ||
		contract.MaxRuntime < 0 || contract.MaxRuntime > MaxRuntimeCeiling ||
		!canonicalList(contract.AllowedEmployeeIDs) || !canonicalList(contract.AllowedModels) {
		return ErrInvalidContract
	}
	return nil
}

// EffectiveMaxParallelTasks is what callers should dispatch against: an
// unset (zero-value / pre-ADR-0051) Contract behaves as
// DefaultMaxParallelTasks, never as "0 concurrency allowed".
func (contract Contract) EffectiveMaxParallelTasks() int {
	if contract.MaxParallelTasks <= 0 {
		return DefaultMaxParallelTasks
	}
	return contract.MaxParallelTasks
}

// EffectiveMaxRevisionCount is what callers should enforce per Task branch:
// an unset (zero-value / pre-Revision-Guard) Contract behaves as
// DefaultMaxRevisionCount, never as "0 revisions allowed".
func (contract Contract) EffectiveMaxRevisionCount() int {
	if contract.MaxRevisionCount <= 0 {
		return DefaultMaxRevisionCount
	}
	return contract.MaxRevisionCount
}

// EffectiveMaxProviderCalls is what callers should enforce for one
// Reviewed Workflow execution: an unset (zero-value / pre-BudgetGuard)
// Contract behaves as DefaultMaxProviderCalls, never as "0 Provider calls
// allowed".
func (contract Contract) EffectiveMaxProviderCalls() int {
	if contract.MaxProviderCalls <= 0 {
		return DefaultMaxProviderCalls
	}
	return contract.MaxProviderCalls
}

// EffectiveMaxRuntime is what callers should enforce for one Reviewed
// Workflow execution: an unset (zero-value / pre-BudgetGuard) Contract
// behaves as DefaultMaxRuntime, never as "0 runtime allowed".
func (contract Contract) EffectiveMaxRuntime() time.Duration {
	if contract.MaxRuntime <= 0 {
		return DefaultMaxRuntime
	}
	return contract.MaxRuntime
}

func (contract Contract) AllowsEmployee(employeeID string) bool {
	return contains(contract.AllowedEmployeeIDs, strings.TrimSpace(employeeID))
}

func (contract Contract) AllowsModel(model string) bool {
	return contains(contract.AllowedModels, strings.TrimSpace(model))
}

func (contract Contract) Clone() Contract {
	contract.AllowedEmployeeIDs = append([]string(nil), contract.AllowedEmployeeIDs...)
	contract.AllowedModels = append([]string(nil), contract.AllowedModels...)
	return contract
}

func canonical(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func canonicalList(values []string) bool {
	if values == nil || len(values) == 0 {
		return false
	}
	for index, value := range values {
		if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") ||
			(index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func contains(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}
