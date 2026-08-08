package organization

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type identityParityFixture struct {
	Version         string           `json:"version"`
	Employees       []Identity       `json:"employees"`
	Managers        []Identity       `json:"workspace_managers"`
	Reserved        []Identity       `json:"reserved_identities"`
	IdentityAudit   json.RawMessage  `json:"identity_audit"`
	NameValidations []NameValidation `json:"name_validations"`
}

func TestIdentityPolicyMatchesPythonGoldenFixture(t *testing.T) {
	fixture := loadIdentityParityFixture(t)
	if fixture.Version != "v1" {
		t.Fatalf("fixture version = %q", fixture.Version)
	}
	inventory := NewInventory(fixture.Employees, fixture.Managers, fixture.Reserved)
	assertJSONEqual(t, AuditIdentities(inventory, DefaultSimilarityThreshold), fixture.IdentityAudit)

	existingNames := make([]string, 0, len(inventory.Identities))
	for _, identity := range inventory.Identities {
		if identity.Name != "" {
			existingNames = append(existingNames, identity.Name)
		}
	}
	for _, expected := range fixture.NameValidations {
		actual := ValidateName(expected.Name, existingNames, DefaultSimilarityThreshold)
		assertJSONEqual(t, actual, mustMarshalJSON(t, expected))
	}
}

func TestValidateInventoryPreservesPythonOrganizationOrdering(t *testing.T) {
	inventory := NewInventory([]Identity{
		{ID: "DEV-001", Name: "佐藤 蓮", Department: "開発部", Role: "Engineer", Model: "Claude Sonnet 5", Status: "待機中"},
		{ID: "DEV-001", Name: "鈴木 陽菜", Department: "開発部", Role: "Engineer", Status: "待機中"},
		{Name: "高橋 拓海"},
	}, nil, nil)
	want := []ValidationIssue{
		{Type: "missing_fields", Name: "鈴木 陽菜", Fields: []string{"model"}},
		{Type: "missing_fields", Name: "高橋 拓海", Fields: []string{"id", "department", "role", "model", "status"}},
		{Type: "duplicate_id", ID: "DEV-001", Employees: []string{"佐藤 蓮", "鈴木 陽菜"}},
	}
	if got := ValidateInventory(inventory); !reflect.DeepEqual(got, want) {
		t.Fatalf("ValidateInventory() = %#v, want %#v", got, want)
	}
}

func TestIdentityNamePolicyMatchesPythonReferenceCases(t *testing.T) {
	existing := []string{"田中 美咲", "高橋 拓海", "佐々木 健太郎"}
	tests := []struct {
		name       string
		allowed    bool
		issueTypes []string
	}{
		{"田中　美咲", false, []string{"normalized_match"}},
		{"田中美咲", false, []string{"invalid_name_format", "normalized_match"}},
		{"optional 太郎", false, []string{"invalid_term", "invalid_name_format"}},
		{"田中 蓮", true, []string{"same_surname"}},
		{"佐々木 健太朗", true, []string{"same_surname", "high_similarity"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ValidateName(test.name, existing, DefaultSimilarityThreshold)
			gotTypes := make([]string, 0, len(result.Issues))
			for _, issue := range result.Issues {
				gotTypes = append(gotTypes, issue.Type)
			}
			if result.Allowed != test.allowed || !reflect.DeepEqual(gotTypes, test.issueTypes) {
				t.Fatalf("ValidateName() = %#v", result)
			}
		})
	}
	if got := Similarity("佐々木 健太朗", "佐々木 健太郎"); got != 0.833 {
		t.Fatalf("Similarity() = %v", got)
	}
}

func TestIdentityAuditIncludesManagersAndReservationsWithoutEmployeeCount(t *testing.T) {
	inventory := NewInventory(
		[]Identity{{ID: "PLAN-001", Name: "田中 美咲"}},
		[]Identity{{ID: "MGR-001", Name: "中村 美咲"}},
		[]Identity{{ID: "BOARD-001", Name: "山田 太郎"}},
	)
	audit := AuditIdentities(inventory, DefaultSimilarityThreshold)
	if audit.EmployeeCount != 1 || audit.IdentityCount != 3 || audit.WorkspaceManagerCount != 1 || audit.ReservedIdentityCount != 1 ||
		len(audit.SameGivenNames) != 1 || audit.SameGivenNames[0].Value != "美咲" ||
		len(audit.RecommendedRetainedIdentities) != 1 || audit.RecommendedRetainedIdentities[0].ID != "MGR-001" {
		t.Fatalf("AuditIdentities() = %#v", audit)
	}
	encoded, err := json.Marshal(audit.SameGivenNames[0])
	if err != nil || string(encoded) == "" || !json.Valid(encoded) {
		t.Fatalf("named group JSON = %s, %v", encoded, err)
	}
}

func TestValidateCandidatesChecksCandidateToCandidateNamesAndAllIdentityIDs(t *testing.T) {
	inventory := NewInventory([]Identity{{ID: "PLAN-001", Name: "田中 美咲"}}, []Identity{{ID: "MGR-001", Name: "中村 真帆"}}, nil)
	validations, err := ValidateCandidates(inventory, []EmployeeCandidate{{ID: "DEV-001", Name: "佐藤 蓮"}, {ID: "QA-001", Name: "高橋 陽菜"}})
	if err != nil || len(validations) != 2 {
		t.Fatalf("validations=%#v err=%v", validations, err)
	}
	if _, err := ValidateCandidates(inventory, []EmployeeCandidate{{ID: "MGR-001", Name: "山本 拓海"}}); err == nil {
		t.Fatal("manager ID accepted")
	}
	if _, err := ValidateCandidates(inventory, []EmployeeCandidate{{ID: "DEV-001", Name: "佐藤 蓮"}, {ID: "QA-001", Name: "鈴木 蓮"}}); err == nil {
		t.Fatal("same candidate given name accepted")
	}
}

func TestBuildIDRepairPlanMatchesPythonAndReservesAllIdentityIDs(t *testing.T) {
	inventory := NewInventory(
		[]Identity{
			{ID: "DEV-002", Name: "佐藤 蓮"},
			{ID: "DEV-002", Name: "鈴木 陽菜"},
			{ID: "DEV-003", Name: "高橋 拓海"},
		},
		[]Identity{{ID: "DEV-004", Name: "Manager"}}, nil,
	)
	want := []IDRepair{{Name: "鈴木 陽菜", CurrentID: "DEV-002", ProposedID: "DEV-005"}}
	if got := BuildIDRepairPlan(inventory); !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildIDRepairPlan() = %#v, want %#v", got, want)
	}
}

func TestValidateRenameBatchReservesCandidateNamesBeforeWrites(t *testing.T) {
	inventory := NewInventory([]Identity{{ID: "PLAN-001", Name: "田中 美咲"}, {ID: "QA-001", Name: "鈴木 健太"}}, nil, nil)
	requests := []RenameRequest{{EmployeeID: "PLAN-001", OldName: "田中 美咲", NewName: "山本 真帆", Reason: "r"}, {EmployeeID: "QA-001", OldName: "鈴木 健太", NewName: "松本 直樹", Reason: "r"}}
	if validations, err := ValidateRenameBatch(inventory, requests); err != nil || len(validations) != 2 {
		t.Fatalf("validations=%#v err=%v", validations, err)
	}
	requests[1].NewName = "山本 真帆"
	if _, err := ValidateRenameBatch(inventory, requests); err == nil {
		t.Fatal("duplicate candidate rename name accepted")
	}
}

func loadIdentityParityFixture(t *testing.T) identityParityFixture {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "organization", "identity_parity_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture identityParityFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertJSONEqual(t *testing.T, actual any, expectedJSON []byte) {
	t.Helper()
	actualJSON := mustMarshalJSON(t, actual)
	var actualValue, expectedValue any
	if err := json.Unmarshal(actualJSON, &actualValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expectedJSON, &expectedValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualValue, expectedValue) {
		t.Fatalf("JSON mismatch\nactual:   %s\nexpected: %s", actualJSON, expectedJSON)
	}
}
