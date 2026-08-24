// Package planningacceptance evaluates CEO Plan decomposition quality
// without owning or mutating product state. It reuses the existing
// production Planning path (process.GenerateCEOPlan) read-only, exactly as
// internal/synthesisacceptance reuses process.ExecuteReviewedWorkflow.
package planningacceptance

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const ScenarioSchemaVersion = "workcairn-planning-acceptance.v1"

var ErrInvalidScenario = errors.New("invalid Planning acceptance scenario")

//go:embed scenario_v1.json
var scenarioV1JSON []byte

// Scenario is the fixed, Provider-neutral data a Planning Acceptance run
// checks a generated ceoplan.Plan against. It holds what the four v1 rubric
// axes need, plus (PHASE T-0) the two named Provider response fixtures
// ("good"/"bad") the CLI selects by name -- mirroring
// internal/synthesisacceptance's ProviderBaselines shape exactly, since a
// production CLI (not just Go tests) now needs to reach them. Additional,
// more narrowly axis-isolating bad fixtures stay as Go test-only constants
// in harness_test.go; only the two canonical baselines are CLI-selectable,
// the same asymmetry Synthesis Acceptance already has.
type Scenario struct {
	SchemaVersion string            `json:"schema_version"`
	ScenarioID    string            `json:"scenario_id"`
	Language      string            `json:"language"`
	CEORequest    string            `json:"ceo_request"`
	Employees     []EmployeeFixture `json:"employees"`
	// ExpectedIntentConceptGroups is Intent Coverage's evaluated set: each
	// inner group is an OR-of-synonyms for one distinct element of the CEO's
	// request (mirrors internal/synthesisacceptance's ConceptGroups shape,
	// the one part of that package's design genuinely reusable as a pattern
	// -- not as code, since what text it is matched against differs).
	ExpectedIntentConceptGroups [][]string `json:"expected_intent_concept_groups"`
	// ExpectedDependencyPositions is Dependency Quality's expected graph
	// shape: index i lists the 0-based positions (in generated-Task order,
	// review steps excluded) task i is expected to depend on. This is a
	// structural comparison target, not a literal-text concept -- a
	// different evaluation primitive than Synthesis's term matching,
	// deliberately not copied from it.
	ExpectedDependencyPositions [][]int  `json:"expected_dependency_positions"`
	ForbiddenClaims             []string `json:"forbidden_claims"`
	// ExpectedMissingInformationConceptGroup is the one concept Missing
	// Information Awareness checks CEOQuestions against: this scenario
	// deliberately never states a success metric for the feature, so a
	// genuinely good Intent should ask about it.
	ExpectedMissingInformationConceptGroup []string                   `json:"expected_missing_information_concept_group"`
	ProviderFixtures                       map[string]ProviderFixture `json:"provider_fixtures"`
}

type EmployeeFixture struct {
	ID         string `json:"id"`
	Department string `json:"department"`
	Role       string `json:"role"`
	Model      string `json:"model"`
	Status     string `json:"status"`
}

// ProviderFixture is a fixed Anthropic Messages API response envelope,
// exactly the shape internal/synthesisacceptance.ProviderBaseline already
// uses -- Body is the full response body (content/usage/stop_reason), not
// just the unwrapped Intent JSON, so it can be served byte-for-byte by a
// Fake HTTP transport.
type ProviderFixture struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func LoadScenario() (Scenario, error) {
	decoder := json.NewDecoder(strings.NewReader(string(scenarioV1JSON)))
	decoder.DisallowUnknownFields()
	var scenario Scenario
	if err := decoder.Decode(&scenario); err != nil {
		return Scenario{}, fmt.Errorf("%w: %v", ErrInvalidScenario, err)
	}
	if err := scenario.Validate(); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}

func (scenario Scenario) Validate() error {
	if scenario.SchemaVersion != ScenarioSchemaVersion || strings.TrimSpace(scenario.ScenarioID) == "" ||
		scenario.Language != "ja" || strings.TrimSpace(scenario.CEORequest) == "" ||
		len(scenario.Employees) == 0 || len(scenario.ExpectedIntentConceptGroups) == 0 ||
		len(scenario.ExpectedDependencyPositions) == 0 || len(scenario.ForbiddenClaims) == 0 ||
		len(scenario.ExpectedMissingInformationConceptGroup) == 0 {
		return ErrInvalidScenario
	}
	seenIDs := map[string]bool{}
	for _, employee := range scenario.Employees {
		if strings.TrimSpace(employee.ID) == "" || strings.TrimSpace(employee.Department) == "" ||
			strings.TrimSpace(employee.Role) == "" || strings.TrimSpace(employee.Model) == "" ||
			strings.TrimSpace(employee.Status) == "" || seenIDs[employee.ID] {
			return ErrInvalidScenario
		}
		seenIDs[employee.ID] = true
	}
	for _, group := range scenario.ExpectedIntentConceptGroups {
		if !validTerms(group) {
			return ErrInvalidScenario
		}
	}
	if !validTerms(scenario.ForbiddenClaims) || !validTerms(scenario.ExpectedMissingInformationConceptGroup) {
		return ErrInvalidScenario
	}
	for _, name := range []string{"good", "bad"} {
		fixture, ok := scenario.ProviderFixtures[name]
		if !ok || fixture.Status < 100 || fixture.Status > 599 || len(fixture.Body) == 0 {
			return ErrInvalidScenario
		}
	}
	return nil
}

func validTerms(terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	for _, term := range terms {
		if strings.TrimSpace(term) == "" {
			return false
		}
	}
	return true
}
