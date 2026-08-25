package planningacceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	workspaceruntime "github.com/AkiraShimizu0/workcairn/go/internal/runtime"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

const (
	ProviderFakeGood = "fake-good"
	ProviderFakeBad  = "fake-bad"
	ProviderClaude   = "claude"

	StructuralGatePassed = "passed"
	StructuralGateFailed = "failed"
)

var (
	ErrInvalidConfig = errors.New("invalid Planning acceptance configuration")
	// ErrProviderCallLimitReached means the harness's own one-shot guard
	// rejected a second Provider call -- never a retry, never a fallback,
	// mirroring internal/synthesisacceptance's identical discipline.
	ErrProviderCallLimitReached = errors.New("Planning acceptance Provider call limit reached")
)

// Config mirrors internal/synthesisacceptance.Config's shape (Provider /
// Execute / APIKey / HTTPClient / VaultRoot / ArtifactPath) so both
// Acceptance gates read the same way, without sharing implementation code
// (PHASE T-0 explicitly does not extract a shared framework).
//
// HTTPClient is an escape hatch: when set, it is always used verbatim
// (this is how the axis-isolating bad fixtures in harness_test.go inject
// arbitrary content) regardless of Provider. When unset, Provider selects
// a named fixture ("fake-good"/"fake-bad", served from the Scenario's own
// embedded ProviderFixtures) or requires a real HTTPClient+APIKey
// ("claude", Execute=true only -- no PHASE has authorized a real call
// through this Config shape yet).
type Config struct {
	Provider     string
	Execute      bool
	APIKey       string
	HTTPClient   claude.HTTPDoer
	VaultRoot    string
	ArtifactPath string
}

// TokenUsage mirrors internal/synthesisacceptance.TokenUsage's shape --
// worker.TokenUsage's nilable pointer fields collapse to a plain Known
// bool for a cleaner safe-report JSON shape.
type TokenUsage struct {
	Known        bool `json:"known"`
	InputTokens  int  `json:"input_tokens,omitempty"`
	OutputTokens int  `json:"output_tokens,omitempty"`
}

// ReviewArtifact is the optional, explicitly-requested-only file a human
// reviewer can inspect after a run: the canonical Normalized Plan (not raw
// pre-normalization Intent -- the Plan is what Go actually committed to
// evaluating, the same "final canonical artifact, not an intermediate
// stage" choice internal/synthesisacceptance's ReviewArtifact.Deliverable
// already made) alongside the same safe metadata already in Result. Never
// written unless Config.ArtifactPath is set; never contains a credential,
// Authorization header, or raw Provider request.
type ReviewArtifact struct {
	ScenarioID           string       `json:"scenario_id"`
	Provider             string       `json:"provider"`
	Model                string       `json:"model"`
	Plan                 ceoplan.Plan `json:"plan"`
	Evaluation           Evaluation   `json:"evaluation"`
	MaxOutputTokens      int          `json:"max_output_tokens"`
	TokenUsage           TokenUsage   `json:"token_usage"`
	DurationMilliseconds int64        `json:"duration_ms"`
	StopReason           string       `json:"stop_reason,omitempty"`
}

// Result is the safe, credential-free report every run returns. TokenUsage/
// Duration/StopReason/Runner are populated only on a successful generation
// (service.CEOPlanResult is always empty on any *service.CEOPlanError,
// including the PHASE R output-incomplete case -- see this file's
// structuralFailureReason -- so those fields stay at their zero value on
// any Structural Gate failure; extending CEOPlanError itself to preserve
// partial diagnostics on failure is a real but separate-scope
// improvement, not made by this Checkpoint).
type Result struct {
	ScenarioID              string      `json:"scenario_id"`
	Provider                string      `json:"provider"`
	Model                   string      `json:"model"`
	LogicalRoute            string      `json:"logical_route"`
	Executed                bool        `json:"executed"`
	Status                  string      `json:"status"`
	ProviderInvocations     int         `json:"provider_invocations"`
	StructuralGate          string      `json:"structural_gate,omitempty"`
	StructuralFailureReason string      `json:"structural_failure_reason,omitempty"`
	Runner                  string      `json:"runner,omitempty"`
	MaxOutputTokens         int         `json:"max_output_tokens"`
	TokenUsage              TokenUsage  `json:"token_usage"`
	DurationMilliseconds    int64       `json:"duration_ms,omitempty"`
	StopReason              string      `json:"stop_reason,omitempty"`
	Evaluation              *Evaluation `json:"evaluation,omitempty"`
	// Parse is the existing, ADR-0041 failure.ParseDiagnostic -- set only
	// when StructuralFailureReason is CEOPlanIntentStage, mirroring the
	// same typed diagnostic the production Interaction flow already
	// surfaces for this identical failure (process/interaction.go's
	// finishInteractionPlan). nil for every other Structural Gate failure
	// and for a successful run. Never carries raw Provider content --
	// entirely sourced from ceoplan.IntentParseError's own safe fields.
	Parse *failure.ParseDiagnostic `json:"parse,omitempty"`
}

// Run exercises the real production Planning path
// (process.GenerateCEOPlan -- the same function cmd/workcairn's
// ceo-plan-generate operation and the Interaction plan.generate Command
// both call) against the fixed scenario roster, then separates Structural
// Gate correctness (existing ceoplan invariants, unmodified) from Quality
// Rubric scoring (this package's own evaluator). It never calls
// process.ExecuteCEOPlanApply, so it never creates a Project, Task, or
// Dependency record.
func Run(ctx context.Context, config Config) (Result, error) {
	scenario, err := LoadScenario()
	if err != nil {
		return Result{StructuralGate: StructuralGateFailed}, err
	}
	provider := strings.TrimSpace(config.Provider)
	if provider == "" {
		provider = ProviderFakeGood
	}
	if provider != ProviderFakeGood && provider != ProviderFakeBad && provider != ProviderClaude {
		return Result{ScenarioID: scenario.ScenarioID, Provider: provider}, fmt.Errorf("%w: unsupported provider %q", ErrInvalidConfig, provider)
	}
	resolution, err := claude.ResolveModel("")
	if err != nil {
		return Result{ScenarioID: scenario.ScenarioID, Provider: provider}, err
	}
	result := Result{
		ScenarioID: scenario.ScenarioID, Provider: provider, Model: resolution.ProviderModel,
		LogicalRoute: resolution.LogicalRoute, MaxOutputTokens: workspaceruntime.DefaultClaudeMaxTokens,
	}

	if provider == ProviderClaude && !config.Execute {
		if _, buildErr := ceoplan.BuildPrompt(scenario.CEORequest, scenarioEmployees(scenario)); buildErr != nil {
			result.Status = "failed"
			return result, buildErr
		}
		if _, schemaErr := ceoplan.IntentJSONSchema(ceoplan.CanonicalRoleTitles(scenarioEmployees(scenario))); schemaErr != nil {
			result.Status = "failed"
			return result, schemaErr
		}
		result.Status = "dry_run_ready"
		return result, nil
	}

	transport, apiKey, err := resolveTransport(config, scenario, provider)
	if err != nil {
		result.Status = "failed"
		return result, err
	}

	root, cleanup, err := acceptanceRoot(config.VaultRoot)
	if err != nil {
		result.Status = "failed"
		return result, err
	}
	defer cleanup()
	if err := prepareRoster(root, scenario); err != nil {
		result.Status = "failed"
		return result, err
	}

	doer := &countingHTTPDoer{inner: transport}
	generation, genErr := workspaceprocess.GenerateCEOPlan(ctx, workspaceprocess.CEOPlanGenerationInput{
		VaultRoot: root, Request: scenario.CEORequest, Model: claude.AutomaticRoute, Approved: true,
	}, workspaceprocess.ClaudeProcessConfig{
		APIKey: apiKey, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
	}, doer)
	result.Executed = true
	result.ProviderInvocations = doer.count()

	if genErr != nil {
		result.Status = "failed"
		result.StructuralGate = StructuralGateFailed
		result.StructuralFailureReason = structuralFailureReason(genErr)
		result.Parse = ceoPlanIntentParseDiagnostic(genErr)
		return result, genErr
	}
	if invariantErr := confirmStructuralInvariants(generation.Plan); invariantErr != nil {
		result.Status = "failed"
		result.StructuralGate = StructuralGateFailed
		result.StructuralFailureReason = invariantErr.Error()
		return result, invariantErr
	}
	result.StructuralGate = StructuralGatePassed
	result.Runner = generation.Runner
	result.TokenUsage = tokenUsage(generation.Usage)
	result.DurationMilliseconds = generation.Duration / 1_000_000
	result.StopReason = string(generation.StopReason)

	evaluation := Evaluate(scenario, generation.Plan)
	result.Evaluation = &evaluation
	result.Status = "evaluated"

	if strings.TrimSpace(config.ArtifactPath) != "" {
		if writeErr := writeReviewArtifact(config.ArtifactPath, ReviewArtifact{
			ScenarioID: scenario.ScenarioID, Provider: provider, Model: resolution.ProviderModel,
			Plan: generation.Plan, Evaluation: evaluation, MaxOutputTokens: result.MaxOutputTokens,
			TokenUsage: result.TokenUsage, DurationMilliseconds: result.DurationMilliseconds, StopReason: result.StopReason,
		}); writeErr != nil {
			result.Status = "failed"
			return result, writeErr
		}
	}
	return result, nil
}

// resolveTransport picks the HTTP transport and API key a call will use.
// An explicitly supplied HTTPClient (Config.HTTPClient) always wins,
// regardless of Provider -- this is how harness_test.go's
// axis-isolating bad fixtures inject arbitrary content without needing a
// named scenario fixture. Otherwise, "fake-good"/"fake-bad" resolve to the
// Scenario's own embedded ProviderFixtures, and "claude" requires the
// caller to supply both APIKey and HTTPClient explicitly (never a silent
// default, never a fallback).
func resolveTransport(config Config, scenario Scenario, provider string) (claude.HTTPDoer, string, error) {
	if config.HTTPClient != nil {
		apiKey := strings.TrimSpace(config.APIKey)
		if apiKey == "" {
			apiKey = "planning-acceptance-fixture-key"
		}
		return config.HTTPClient, apiKey, nil
	}
	switch provider {
	case ProviderFakeGood, ProviderFakeBad:
		name := "good"
		if provider == ProviderFakeBad {
			name = "bad"
		}
		fixture, ok := scenario.ProviderFixtures[name]
		if !ok {
			return nil, "", fmt.Errorf("%w: scenario is missing the %q provider fixture", ErrInvalidScenario, name)
		}
		return FixedResponseHTTPDoer{Status: fixture.Status, Headers: fixture.Headers, Body: []byte(fixture.Body)}, "planning-acceptance-fixture-key", nil
	case ProviderClaude:
		return nil, "", fmt.Errorf("%w: APIKey and HTTPClient are required for explicit claude execution", ErrInvalidConfig)
	default:
		return nil, "", fmt.Errorf("%w: unsupported provider %q", ErrInvalidConfig, provider)
	}
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
// it only reads the Stage the production code already computes. This
// includes CEOPlanOutputIncompleteStage (PHASE R) exactly like any other
// stage; no special-casing is added here for it.
func structuralFailureReason(err error) string {
	var planErr *service.CEOPlanError
	if errors.As(err, &planErr) {
		return string(planErr.Stage)
	}
	return "unknown"
}

// ceoPlanIntentParseDiagnostic extracts the existing, ADR-0041
// failure.ParseDiagnostic from a CEOPlanIntentStage failure, mirroring
// process/interaction.go's identical extraction for the production
// Interaction flow (ceoPlanParseFailureReason/Field/FieldShape) -- not
// shared code (those helpers are unexported in package process), but the
// same errors.As pattern over ceoplan.IntentParseError's own safe fields.
// Returns nil for every other failure kind (Runner, Normalization,
// Timeout, Canceled, OutputIncomplete), since only a ParseIntent failure
// ever produces an *ceoplan.IntentParseError in the error chain.
func ceoPlanIntentParseDiagnostic(err error) *failure.ParseDiagnostic {
	var intentErr *ceoplan.IntentParseError
	if !errors.As(err, &intentErr) {
		return nil
	}
	return &failure.ParseDiagnostic{
		Domain: "ceo_plan_intent", Reason: string(intentErr.Reason), Field: intentErr.Field,
		StructuredOutputFieldShape: intentErr.FieldShape,
	}
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

func scenarioEmployees(scenario Scenario) []organization.Identity {
	employees := make([]organization.Identity, 0, len(scenario.Employees))
	for _, fixture := range scenario.Employees {
		employees = append(employees, organization.Identity{
			ID: fixture.ID, Department: fixture.Department, Role: fixture.Role,
			Model: fixture.Model, Status: fixture.Status,
		})
	}
	return employees
}

func tokenUsage(usage worker.TokenUsage) TokenUsage {
	result := TokenUsage{}
	if usage.InputTokens != nil && usage.OutputTokens != nil {
		result.Known = true
		result.InputTokens = *usage.InputTokens
		result.OutputTokens = *usage.OutputTokens
	}
	return result
}

// writeReviewArtifact writes the Human Review Artifact as pretty-printed
// JSON. It never restricts or defaults the path -- Run's own caller is
// responsible for choosing a location outside the Git working tree and
// outside a real Vault (see Config.ArtifactPath) -- and it writes nothing
// beyond the file's own content: no credential, header, or Provider
// request is ever part of ReviewArtifact's shape.
func writeReviewArtifact(path string, artifact ReviewArtifact) error {
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
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
// regardless of request content.
type FixedResponseHTTPDoer struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

func (fixed FixedResponseHTTPDoer) Do(request *http.Request) (*http.Response, error) {
	status := fixed.Status
	if status == 0 {
		status = http.StatusOK
	}
	header := make(http.Header)
	for key, value := range fixed.Headers {
		header.Set(key, value)
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	return &http.Response{
		StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(fixed.Body)), Request: request,
	}, nil
}
