package synthesisacceptance

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
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/workcairn/go/internal/deliverable"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	"github.com/AkiraShimizu0/workcairn/go/internal/project"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

const (
	ProviderFakeGood = "fake-good"
	ProviderFakeBad  = "fake-bad"
	ProviderClaude   = "claude"

	FailureContext  = "CONTEXT_FAILURE"
	FailureProvider = "PROVIDER_FAILURE"
	FailureQuality  = "QUALITY_FAILURE"
	FailureHarness  = "HARNESS_FAILURE"

	acceptanceMaxProviderCalls = 2
	acceptanceMaxRuntime       = 10 * time.Minute
)

var (
	ErrInvalidConfig = errors.New("invalid Synthesis acceptance configuration")
	ErrFailed        = errors.New("Synthesis acceptance failed")
)

type Config struct {
	Provider   string
	Execute    bool
	APIKey     string
	HTTPClient claude.HTTPDoer
	VaultRoot  string
}

type TokenUsage struct {
	Known        bool `json:"known"`
	InputTokens  int  `json:"input_tokens,omitempty"`
	OutputTokens int  `json:"output_tokens,omitempty"`
}

type Result struct {
	ScenarioID             string            `json:"scenario_id"`
	Provider               string            `json:"provider"`
	LogicalRoute           string            `json:"logical_route"`
	Model                  string            `json:"model"`
	Executed               bool              `json:"executed"`
	Ready                  bool              `json:"ready"`
	Passed                 bool              `json:"passed"`
	Status                 string            `json:"status"`
	FailureCategory        string            `json:"failure_category,omitempty"`
	Evaluation             *Evaluation       `json:"evaluation,omitempty"`
	Prompt                 PromptObservation `json:"prompt"`
	OutputBytes            int               `json:"output_bytes,omitempty"`
	TokenUsage             TokenUsage        `json:"token_usage"`
	DurationMilliseconds   int64             `json:"duration_ms"`
	CanonicalDeliverable   bool              `json:"canonical_deliverable"`
	ProviderInvocations    int               `json:"provider_invocations"`
	ExternalProviderCalls  int               `json:"external_provider_calls"`
	MaxProviderCalls       int               `json:"max_provider_calls"`
	MaxRuntimeMilliseconds int64             `json:"max_runtime_ms"`
}

func Run(ctx context.Context, config Config) (Result, error) {
	scenario, err := LoadScenario()
	if err != nil {
		return Result{FailureCategory: FailureHarness, Status: "failed"}, err
	}
	config.Provider = strings.TrimSpace(config.Provider)
	if config.Provider == "" {
		config.Provider = ProviderFakeGood
	}
	resolution, err := claude.ResolveModel(claude.AutomaticRoute)
	if err != nil {
		return Result{ScenarioID: scenario.ScenarioID, Provider: config.Provider, FailureCategory: FailureHarness, Status: "failed"}, err
	}
	result := Result{
		ScenarioID: scenario.ScenarioID, Provider: config.Provider, LogicalRoute: resolution.LogicalRoute, Model: resolution.ProviderModel,
		Status: "dry_run_ready", Ready: true, MaxProviderCalls: acceptanceMaxProviderCalls,
		MaxRuntimeMilliseconds: acceptanceMaxRuntime.Milliseconds(),
	}
	if config.Provider != ProviderFakeGood && config.Provider != ProviderFakeBad && config.Provider != ProviderClaude {
		result.Ready, result.Status, result.FailureCategory = false, "failed", FailureHarness
		return result, fmt.Errorf("%w: unsupported provider %q", ErrInvalidConfig, config.Provider)
	}

	if !config.Execute {
		prompt, buildErr := BuildScenarioPrompt(scenario, scenario.Evidence)
		if buildErr != nil {
			result.Ready, result.Status, result.FailureCategory = false, "failed", FailureContext
			return result, buildErr
		}
		observation, observeErr := ObservePrompt(prompt, scenario)
		result.Prompt = observation
		if observeErr != nil {
			result.Ready, result.Status, result.FailureCategory = false, "failed", FailureContext
			return result, observeErr
		}
		return result, nil
	}
	if config.Provider == ProviderClaude && (strings.TrimSpace(config.APIKey) == "" || config.HTTPClient == nil) {
		result.Ready, result.Status, result.FailureCategory = false, "failed", FailureHarness
		return result, fmt.Errorf("%w: Claude credential and HTTP client are required only for explicit execution", ErrInvalidConfig)
	}

	root, cleanup, err := acceptanceRoot(config.VaultRoot)
	if err != nil {
		result.Ready, result.Status, result.FailureCategory = false, "failed", FailureHarness
		return result, err
	}
	defer cleanup()
	if err := prepareCanonicalScenario(ctx, root, scenario); err != nil {
		result.Ready, result.Status, result.FailureCategory = false, "failed", FailureHarness
		return result, err
	}

	provider := &observingProvider{
		scenario: scenario, mode: config.Provider, external: config.HTTPClient,
	}
	apiKey := "synthesis-acceptance-fixture-key"
	if config.Provider == ProviderClaude {
		apiKey = config.APIKey
	}
	contract, err := autonomy.NewStandard([]string{"SYNTH-001", "QA-001"}, []string{claude.AutomaticRoute}, 4)
	if err != nil {
		return result, err
	}
	contract.MaxProviderCalls = acceptanceMaxProviderCalls
	contract.MaxRuntime = acceptanceMaxRuntime
	at := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	workflowResult, workflowErr := workspaceprocess.ExecuteReviewedWorkflow(ctx, workspaceprocess.ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: workspaceprocess.ReviewedWorkflowPlanInput{
			WorkflowPlanInput: workspaceprocess.WorkflowPlanInput{
				VaultRoot: root, ProjectID: "PROJECT-SYNTHESIS-001", ProjectName: scenario.ProjectName, CurrentTime: at,
			},
			ReviewerID: "QA-001",
		},
		Approved: true, ApprovalReference: "synthesis-quality-acceptance", CommandID: "CMD-SYNTHESIS-ACCEPTANCE-001",
		MaxTasks: 1, Autonomy: contract, CorrelationID: "CMD-SYNTHESIS-ACCEPTANCE-001",
	}, workspaceprocess.ClaudeProcessConfig{APIKey: apiKey}, provider)
	result.Executed = true
	result.ProviderInvocations, result.ExternalProviderCalls = provider.counts()

	prompt, ok := provider.synthesisPrompt()
	if !ok {
		result.Ready, result.Status, result.FailureCategory = false, "failed", FailureContext
		if workflowErr != nil {
			return result, fmt.Errorf("%w: %v", ErrContextFailure, workflowErr)
		}
		return result, fmt.Errorf("%w: Synthesis Provider request was not observed", ErrContextFailure)
	}
	observation, observeErr := ObservePrompt(prompt, scenario)
	result.Prompt = observation
	if observeErr != nil {
		result.Ready, result.Status, result.FailureCategory = false, "failed", FailureContext
		return result, observeErr
	}
	if workflowErr != nil {
		result.Ready, result.Status = false, "failed"
		result.FailureCategory = workflowFailureCategory(workflowResult)
		return result, fmt.Errorf("%w: %v", ErrFailed, workflowErr)
	}

	evidence, err := vault.InspectTaskEvidence(ctx, root, scenario.ProjectName, "TASK-004")
	if err != nil || evidence.Deliverable == nil || evidence.Task.Status != task.StatusCompleted {
		result.Ready, result.Status, result.FailureCategory = false, "failed", FailureHarness
		if err == nil {
			err = errors.New("canonical Synthesis Deliverable was not committed")
		}
		return result, err
	}
	result.CanonicalDeliverable = true
	result.OutputBytes = len([]byte(evidence.Deliverable.Content))
	evaluation := Evaluate(scenario, evidence.Deliverable.Content)
	result.Evaluation = &evaluation
	for _, current := range workflowResult.Tasks {
		if current.TaskID == "TASK-004" {
			result.DurationMilliseconds = current.Execution.Duration.Milliseconds()
			result.TokenUsage = tokenUsage(current.Execution.Usage)
			break
		}
	}
	result.Passed = evaluation.Passed
	if !evaluation.Passed {
		result.Status, result.FailureCategory = "failed", FailureQuality
		return result, ErrFailed
	}
	result.Status = "passed"
	return result, nil
}

func acceptanceRoot(configured string) (string, func(), error) {
	if strings.TrimSpace(configured) != "" {
		absolute, err := filepath.Abs(configured)
		return absolute, func() {}, err
	}
	root, err := os.MkdirTemp("", "workcairn-synthesis-acceptance-*")
	if err != nil {
		return "", func() {}, err
	}
	return root, func() { _ = os.RemoveAll(root) }, nil
}

func prepareCanonicalScenario(ctx context.Context, root string, scenario Scenario) error {
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		return err
	}
	for path, content := range map[string]string{
		filepath.Join(root, "社員", "統合担当.md"): "---\nid: SYNTH-001\ndepartment: 企画部\nrole: Product Manager\nmodel: workcairn-auto\nstatus: 待機中\n---\n",
		filepath.Join(root, "社員", "QA担当.md"): "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: workcairn-auto\nstatus: 待機中\n---\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	at := time.Date(2026, time.August, 21, 11, 0, 0, 0, time.UTC)
	if _, err := workspaceprocess.ExecuteProjectBootstrap(ctx, workspaceprocess.ProjectBootstrapInput{
		VaultRoot: root, ProjectID: "PROJECT-SYNTHESIS-001", ProjectName: scenario.ProjectName,
		Description: scenario.ProjectObjective, CurrentTime: at, CommandID: "CMD-SYNTHESIS-PROJECT-001",
	}, true); err != nil {
		return err
	}
	assignee := "SYNTH-001"
	titles := []string{scenario.Evidence[0].Title, scenario.Evidence[1].Title, scenario.Evidence[2].Title, scenario.SynthesisTask}
	for index, title := range titles {
		created, err := workspaceprocess.ExecuteTaskCreation(ctx, workspaceprocess.TaskCreationInput{
			VaultRoot: root, ProjectName: scenario.ProjectName, Title: title, AssigneeID: &assignee,
			CurrentTime: at.Add(time.Duration(index+1) * time.Minute), CommandID: fmt.Sprintf("CMD-SYNTHESIS-TASK-%03d", index+1),
		}, true)
		if err != nil {
			return err
		}
		want := fmt.Sprintf("TASK-%03d", index+1)
		if created.Task.ID != want {
			return fmt.Errorf("acceptance Task ID = %s, want %s", created.Task.ID, want)
		}
	}
	rows := []project.TaskDependency{
		{TaskID: "TASK-001", ProposalID: "PROPOSED-001", DependsOn: []string{}, Rationale: "user research"},
		{TaskID: "TASK-002", ProposalID: "PROPOSED-002", DependsOn: []string{}, Rationale: "competitive analysis"},
		{TaskID: "TASK-003", ProposalID: "PROPOSED-003", DependsOn: []string{}, Rationale: "metrics analysis"},
		{TaskID: "TASK-004", ProposalID: "PROPOSED-004", DependsOn: []string{"TASK-001", "TASK-002", "TASK-003"}, Rationale: "synthesis"},
	}
	if _, err := workspaceprocess.ExecuteProjectDependencies(ctx, workspaceprocess.ProjectDependenciesInput{
		VaultRoot: root, ProjectName: scenario.ProjectName, Rows: rows,
		CurrentTime: at.Add(10 * time.Minute), CommandID: "CMD-SYNTHESIS-DEPS-001",
	}, true); err != nil {
		return err
	}

	taskStore, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: scenario.ProjectName})
	if err != nil {
		return err
	}
	deliverables, err := vault.NewDeliverableStore(root, scenario.ProjectName)
	if err != nil {
		return err
	}
	events := service.NewEventService(nil)
	tasks, err := service.NewTaskService(taskStore, events)
	if err != nil {
		return err
	}
	if err := events.Start(); err != nil {
		return err
	}
	defer events.Stop()
	if err := tasks.Activate(); err != nil {
		return err
	}
	defer tasks.Deactivate()
	for index, source := range scenario.Evidence {
		if _, err := tasks.Start(ctx, source.TaskID); err != nil {
			return err
		}
		if _, err := deliverables.Save(ctx, deliverable.Document{
			ProjectID: "PROJECT-SYNTHESIS-001", ProjectName: scenario.ProjectName, TaskTitle: source.Title,
			ExecutedAt: at.Add(time.Duration(20+index) * time.Minute),
			Execution: worker.ExecutionResult{
				Content: source.Content, EmployeeID: source.EmployeeID, TaskID: source.TaskID,
				Runner: "AcceptanceFixture", Model: claude.AutomaticRoute, Status: worker.StatusCompleted,
			},
		}); err != nil {
			return err
		}
		if _, err := tasks.Complete(ctx, source.TaskID); err != nil {
			return err
		}
	}
	return nil
}

type observingProvider struct {
	mu            sync.Mutex
	scenario      Scenario
	mode          string
	external      claude.HTTPDoer
	prompt        worker.Prompt
	seen          bool
	total         int
	externalCalls int
}

func (provider *observingProvider) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	var decoded struct {
		Model    string `json:"model"`
		System   string `json:"system"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
		OutputConfig json.RawMessage `json:"output_config"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	provider.mu.Lock()
	provider.total++
	provider.mu.Unlock()
	if len(decoded.OutputConfig) > 0 && string(decoded.OutputConfig) != "null" {
		return provider.fixtureResponse("review_approve", request), nil
	}
	user := ""
	if len(decoded.Messages) == 1 {
		user = decoded.Messages[0].Content
	}
	provider.mu.Lock()
	provider.prompt = worker.Prompt{System: decoded.System, User: user}
	provider.seen = true
	provider.mu.Unlock()
	if provider.mode == ProviderClaude {
		provider.mu.Lock()
		if provider.externalCalls >= 1 {
			provider.mu.Unlock()
			return nil, errors.New("Synthesis acceptance external Provider call limit reached")
		}
		provider.externalCalls++
		provider.mu.Unlock()
		request.Body = io.NopCloser(bytes.NewReader(body))
		return provider.external.Do(request)
	}
	baseline := "good"
	if provider.mode == ProviderFakeBad {
		baseline = "bad_concatenation"
	}
	return provider.fixtureResponse(baseline, request), nil
}

func (provider *observingProvider) fixtureResponse(name string, request *http.Request) *http.Response {
	fixture := provider.scenario.ProviderBaselines[name]
	header := make(http.Header)
	for key, value := range fixture.Headers {
		header.Set(key, value)
	}
	return &http.Response{
		StatusCode: fixture.Status, Header: header, Body: io.NopCloser(bytes.NewReader(fixture.Body)), Request: request,
	}
}

func (provider *observingProvider) synthesisPrompt() (worker.Prompt, bool) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.prompt, provider.seen
}

func (provider *observingProvider) counts() (int, int) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.total, provider.externalCalls
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

func workflowFailureCategory(result service.ReviewedWorkflowRunResult) string {
	for _, current := range result.Tasks {
		if current.Execution.ProviderFailure != nil || current.Review != nil && current.Review.ProviderFailure != nil {
			return FailureProvider
		}
	}
	return FailureHarness
}
