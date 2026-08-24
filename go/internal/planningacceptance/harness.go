package planningacceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	workspaceruntime "github.com/AkiraShimizu0/workcairn/go/internal/runtime"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

const (
	StructuralGatePassed = "passed"
	StructuralGateFailed = "failed"
)

var (
	// ErrProviderCallLimitReached means the harness's own one-shot guard
	// rejected a second Provider call -- never a retry, never a fallback,
	// mirroring internal/synthesisacceptance's identical discipline.
	ErrProviderCallLimitReached = errors.New("Planning acceptance Provider call limit reached")
)

// Config's HTTPClient is deliberately the same claude.HTTPDoer interface
// process.GenerateCEOPlan already takes: today's Checkpoint always passes a
// Fake implementation, but Run() itself has no Fake-only branch -- the
// exact same call would exercise a real Claude connection with a real
// HTTPClient, exactly like internal/synthesisacceptance's Config.HTTPClient.
// Real Provider execution is out of this Checkpoint's scope only because no
// caller supplies real credentials/HTTPClient here yet, not because Run()
// special-cases Fake.
type Config struct {
	VaultRoot  string
	HTTPClient claude.HTTPDoer
}

// Result is intentionally minimal (Step 12): no TokenUsage, Duration,
// StopReason, or MaxOutputTokens field exists yet, because this Checkpoint
// never makes a real Provider call and inventing an observability shape
// with no real data to populate it would repeat the exact
// build-ahead-of-evidence mistake this session's Synthesis Acceptance work
// corrected. Every field here is additive JSON, so a future real-Provider
// Checkpoint can extend this shape without breaking existing callers.
type Result struct {
	ScenarioID              string `json:"scenario_id"`
	Model                   string `json:"model"`
	LogicalRoute            string `json:"logical_route"`
	Executed                bool   `json:"executed"`
	ProviderInvocations     int    `json:"provider_invocations"`
	StructuralGate          string `json:"structural_gate"`
	StructuralFailureReason string `json:"structural_failure_reason,omitempty"`
	// Evaluation is nil whenever StructuralGate is "failed" -- the Quality
	// Rubric never runs on a Plan the Structural Gate already rejected.
	Evaluation *Evaluation `json:"evaluation,omitempty"`
}

// Run exercises the real production Planning path
// (process.GenerateCEOPlan -- the same function cmd/workcairn's
// ceo-plan-generate operation and the Interaction plan.generate Command
// both call) against the fixed scenario roster, then separates Structural
// Gate correctness (existing ceoplan invariants, unmodified) from Quality
// Rubric scoring (this package's own, new evaluator). It never calls
// process.ExecuteCEOPlanApply, so it never creates a Project, Task, or
// Dependency record -- Plan generation alone is sufficient to measure
// Planning quality (see docs/PlanningQualityAcceptance.md).
func Run(ctx context.Context, config Config) (Result, error) {
	scenario, err := LoadScenario()
	if err != nil {
		return Result{StructuralGate: StructuralGateFailed}, err
	}
	if config.HTTPClient == nil {
		return Result{ScenarioID: scenario.ScenarioID, StructuralGate: StructuralGateFailed},
			fmt.Errorf("%w: HTTPClient is required", ErrProviderCallLimitReached)
	}
	resolution, err := claude.ResolveModel("")
	if err != nil {
		return Result{ScenarioID: scenario.ScenarioID, StructuralGate: StructuralGateFailed}, err
	}

	root, cleanup, err := acceptanceRoot(config.VaultRoot)
	if err != nil {
		return Result{ScenarioID: scenario.ScenarioID, StructuralGate: StructuralGateFailed}, err
	}
	defer cleanup()
	if err := prepareRoster(root, scenario); err != nil {
		return Result{ScenarioID: scenario.ScenarioID, StructuralGate: StructuralGateFailed}, err
	}

	doer := &countingHTTPDoer{inner: config.HTTPClient}
	result := Result{ScenarioID: scenario.ScenarioID, Model: resolution.ProviderModel, LogicalRoute: resolution.LogicalRoute}

	generation, genErr := workspaceprocess.GenerateCEOPlan(ctx, workspaceprocess.CEOPlanGenerationInput{
		VaultRoot: root, Request: scenario.CEORequest, Model: claude.AutomaticRoute, Approved: true,
	}, workspaceprocess.ClaudeProcessConfig{
		APIKey: "planning-acceptance-fixture-key", MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
	}, doer)
	result.Executed = true
	result.ProviderInvocations = doer.count()

	if genErr != nil {
		result.StructuralGate = StructuralGateFailed
		result.StructuralFailureReason = structuralFailureReason(genErr)
		return result, genErr
	}
	if invariantErr := confirmStructuralInvariants(generation.Plan); invariantErr != nil {
		result.StructuralGate = StructuralGateFailed
		result.StructuralFailureReason = invariantErr.Error()
		return result, invariantErr
	}
	result.StructuralGate = StructuralGatePassed

	evaluation := Evaluate(scenario, generation.Plan)
	result.Evaluation = &evaluation
	return result, nil
}

// confirmStructuralInvariants is deliberately light: NormalizeCandidate
// (called inside NormalizeIntent, called inside GenerateCEOPlan) already
// enforces canonical Task IDs, dependency existence/self-dependency/cycle
// rejection, and required-field validation -- re-testing that machinery
// here would duplicate ceoplan's own ~40 existing tests (see Phase P
// investigation) rather than add coverage. This only confirms the
// Structural Gate's own contract-level promises to a Planning Acceptance
// caller: the Plan is genuinely plan-only (never committed) and within the
// generated-Task LoopGuard.
func confirmStructuralInvariants(plan ceoplan.Plan) error {
	if !plan.PlanOnly {
		return fmt.Errorf("structural invariant violated: Plan.PlanOnly must be true before apply")
	}
	if len(plan.ProposedTasks) == 0 || len(plan.ProposedTasks) > ceoplan.MaxGeneratedTasks {
		return fmt.Errorf("structural invariant violated: proposed Task count %d outside (0, %d]", len(plan.ProposedTasks), ceoplan.MaxGeneratedTasks)
	}
	for index, task := range plan.ProposedTasks {
		want := fmt.Sprintf("PROPOSED-%03d", index+1)
		if task.ProposalID != want {
			return fmt.Errorf("structural invariant violated: proposal ID %s at position %d, want %s", task.ProposalID, index, want)
		}
		if task.AssigneeID == nil {
			return fmt.Errorf("structural invariant violated: task %s has no resolved assignee", task.ProposalID)
		}
	}
	return nil
}

// structuralFailureReason classifies a GenerateCEOPlan failure using the
// existing typed *service.CEOPlanError this Checkpoint does not modify --
// it only reads the Stage the production code already computes.
func structuralFailureReason(err error) string {
	var planErr *service.CEOPlanError
	if errors.As(err, &planErr) {
		return string(planErr.Stage)
	}
	return "unknown"
}

func acceptanceRoot(configured string) (string, func(), error) {
	if configured != "" {
		return configured, func() {}, nil
	}
	root, err := os.MkdirTemp("", "workcairn-planning-acceptance-*")
	if err != nil {
		return "", func() {}, err
	}
	return root, func() { _ = os.RemoveAll(root) }, nil
}

// prepareRoster writes only Employee Markdown -- no Project, Task, or
// Dependency file is ever created by this package, since GenerateCEOPlan
// itself performs no such writes (see docs/PlanningQualityAcceptance.md,
// "Production path choice").
func prepareRoster(root string, scenario Scenario) error {
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		return err
	}
	for _, employee := range scenario.Employees {
		content := fmt.Sprintf("---\nid: %s\ndepartment: %s\nrole: %s\nmodel: %s\nstatus: %s\n---\n",
			employee.ID, employee.Department, employee.Role, employee.Model, employee.Status)
		path := filepath.Join(root, "社員", employee.ID+".md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type countingHTTPDoer struct {
	mu    sync.Mutex
	inner claude.HTTPDoer
	calls int
}

func (doer *countingHTTPDoer) Do(request *http.Request) (*http.Response, error) {
	doer.mu.Lock()
	doer.calls++
	count := doer.calls
	doer.mu.Unlock()
	if count > 1 {
		return nil, fmt.Errorf("%w: this is call %d, no retry or fallback is permitted", ErrProviderCallLimitReached, count)
	}
	return doer.inner.Do(request)
}

func (doer *countingHTTPDoer) count() int {
	doer.mu.Lock()
	defer doer.mu.Unlock()
	return doer.calls
}

// FixedResponseHTTPDoer is a small, deliberately-not-configurable Fake
// Provider transport: it always returns the same fixed JSON body,
// regardless of request content. Tests construct the actual fixture bodies
// (good/bad Structured Output responses) -- this package owns no named
// fixture registry, since nothing outside tests selects one yet (no CLI or
// Makefile target exists this Checkpoint).
type FixedResponseHTTPDoer struct {
	Body []byte
}

func (fixed FixedResponseHTTPDoer) Do(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(fixed.Body)),
		Request:    request,
	}, nil
}
