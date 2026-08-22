// Package synthesisacceptance evaluates Synthesis quality without owning or
// mutating product state. Production Services create the canonical output;
// this package only prepares sanitized acceptance fixtures and reads results.
package synthesisacceptance

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const ScenarioSchemaVersion = "workcairn-synthesis-acceptance.v1"

var ErrInvalidScenario = errors.New("invalid Synthesis acceptance scenario")

//go:embed scenario_v1.json
var scenarioV1JSON []byte

type Scenario struct {
	SchemaVersion                 string                      `json:"schema_version"`
	ScenarioID                    string                      `json:"scenario_id"`
	Language                      string                      `json:"language"`
	ProjectName                   string                      `json:"project_name"`
	ProjectObjective              string                      `json:"project_objective"`
	SynthesisTask                 string                      `json:"synthesis_task"`
	Evidence                      []Evidence                  `json:"evidence"`
	ExpectedCrossEvidenceInsights []CrossEvidenceInsight      `json:"expected_cross_evidence_insights"`
	Conflict                      Conflict                    `json:"conflict"`
	PriorityMarkers               []string                    `json:"priority_markers"`
	ActionMarkers                 []string                    `json:"action_markers"`
	MeasurementMarkers            []string                    `json:"measurement_markers"`
	ForbiddenClaims               []string                    `json:"forbidden_claims"`
	ProviderBaselines             map[string]ProviderBaseline `json:"provider_baselines"`
}

type Evidence struct {
	ID               string     `json:"id"`
	TaskID           string     `json:"task_id"`
	Title            string     `json:"title"`
	EmployeeID       string     `json:"employee_id"`
	Content          string     `json:"content"`
	KeyConceptGroups [][]string `json:"key_concept_groups"`
}

type CrossEvidenceInsight struct {
	ID            string     `json:"id"`
	ConceptGroups [][]string `json:"concept_groups"`
}

type Conflict struct {
	Description       string   `json:"description"`
	SideA             []string `json:"side_a"`
	SideB             []string `json:"side_b"`
	ResolutionMarkers []string `json:"resolution_markers"`
}

type ProviderBaseline struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func LoadScenario() (Scenario, error) {
	var scenario Scenario
	decoder := json.NewDecoder(strings.NewReader(string(scenarioV1JSON)))
	decoder.DisallowUnknownFields()
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
		scenario.Language != "ja" || strings.TrimSpace(scenario.ProjectName) == "" ||
		strings.TrimSpace(scenario.ProjectObjective) == "" || strings.TrimSpace(scenario.SynthesisTask) == "" ||
		len(scenario.Evidence) != 3 || len(scenario.ExpectedCrossEvidenceInsights) == 0 ||
		len(scenario.Conflict.SideA) == 0 || len(scenario.Conflict.SideB) == 0 || len(scenario.Conflict.ResolutionMarkers) == 0 ||
		len(scenario.PriorityMarkers) == 0 || len(scenario.ActionMarkers) == 0 || len(scenario.MeasurementMarkers) == 0 ||
		len(scenario.ProviderBaselines) < 3 {
		return ErrInvalidScenario
	}
	seenIDs := map[string]bool{}
	seenTasks := map[string]bool{}
	for _, evidence := range scenario.Evidence {
		if strings.TrimSpace(evidence.ID) == "" || strings.TrimSpace(evidence.TaskID) == "" ||
			strings.TrimSpace(evidence.Title) == "" || strings.TrimSpace(evidence.EmployeeID) == "" ||
			strings.TrimSpace(evidence.Content) == "" || len(evidence.KeyConceptGroups) == 0 ||
			seenIDs[evidence.ID] || seenTasks[evidence.TaskID] {
			return ErrInvalidScenario
		}
		seenIDs[evidence.ID] = true
		seenTasks[evidence.TaskID] = true
		for _, group := range evidence.KeyConceptGroups {
			if !validTerms(group) {
				return ErrInvalidScenario
			}
		}
	}
	for _, insight := range scenario.ExpectedCrossEvidenceInsights {
		if strings.TrimSpace(insight.ID) == "" || len(insight.ConceptGroups) < 2 {
			return ErrInvalidScenario
		}
		for _, group := range insight.ConceptGroups {
			if !validTerms(group) {
				return ErrInvalidScenario
			}
		}
	}
	for _, name := range []string{"good", "bad_concatenation", "review_approve"} {
		baseline, ok := scenario.ProviderBaselines[name]
		if !ok || baseline.Status < 100 || baseline.Status > 599 || len(baseline.Body) == 0 {
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

func (scenario Scenario) BaselineOutput(name string) (string, error) {
	baseline, ok := scenario.ProviderBaselines[name]
	if !ok {
		return "", fmt.Errorf("%w: baseline %q", ErrInvalidScenario, name)
	}
	var response struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(baseline.Body, &response); err != nil || len(response.Content) != 1 ||
		response.Content[0].Type != "text" || strings.TrimSpace(response.Content[0].Text) == "" {
		return "", fmt.Errorf("%w: baseline %q response", ErrInvalidScenario, name)
	}
	return response.Content[0].Text, nil
}
