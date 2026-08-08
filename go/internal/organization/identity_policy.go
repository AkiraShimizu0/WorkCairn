package organization

import (
	"math"
	"sort"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const DefaultSimilarityThreshold = 0.8

var invalidTerms = []string{"none", "null", "optional", "unknown", "任意", "未定", "未設定"}
var unicodeCaseFolder = cases.Fold()

type NameIssue struct {
	Type             string   `json:"type"`
	Level            string   `json:"level"`
	BlocksHire       bool     `json:"blocks_hire"`
	Message          string   `json:"message"`
	Terms            []string `json:"terms,omitempty"`
	RelatedEmployees any      `json:"related_employees,omitempty"`
}

type NameValidation struct {
	Name           string      `json:"name"`
	DisplayName    string      `json:"display_name"`
	NormalizedName string      `json:"normalized_name"`
	Allowed        bool        `json:"allowed"`
	Issues         []NameIssue `json:"issues"`
	Errors         []NameIssue `json:"errors"`
	Warnings       []NameIssue `json:"warnings"`
	Reasons        []string    `json:"reasons"`
}

func ValidateName(name string, existingNames []string, similarityThreshold float64) NameValidation {
	if similarityThreshold <= 0 {
		similarityThreshold = DefaultSimilarityThreshold
	}
	displayName := NormalizeDisplayName(name)
	normalizedName := NormalizeName(name)
	issues := make([]NameIssue, 0)
	terms := findInvalidTerms(normalizedName)
	if len(terms) > 0 {
		issues = append(issues, NameIssue{
			Type: "invalid_term", Level: "error", BlocksHire: true,
			Message: "社員名に不正語が含まれています: " + strings.Join(terms, ", "), Terms: terms,
		})
	}
	surname, givenName, validParts := SplitJapaneseName(name)
	if !validParts {
		issues = append(issues, NameIssue{
			Type: "invalid_name_format", Level: "error", BlocksHire: true,
			Message: "日本語の自然な『姓 名』形式ではありません",
		})
	}
	exactMatches := make([]string, 0)
	normalizedMatches := make([]string, 0)
	for _, existing := range existingNames {
		if name == existing {
			exactMatches = append(exactMatches, existing)
		} else if normalizedName == NormalizeName(existing) {
			normalizedMatches = append(normalizedMatches, existing)
		}
	}
	if len(exactMatches) > 0 {
		issues = append(issues, NameIssue{Type: "exact_match", Level: "error", BlocksHire: true, Message: "既存社員と氏名が完全一致しています", RelatedEmployees: exactMatches})
	}
	if len(normalizedMatches) > 0 {
		issues = append(issues, NameIssue{Type: "normalized_match", Level: "error", BlocksHire: true, Message: "空白を正規化すると既存社員名と一致します", RelatedEmployees: normalizedMatches})
	}
	if validParts {
		sameGiven := make([]string, 0)
		sameSurname := make([]string, 0)
		highSimilarity := make([]SimilarName, 0)
		for _, existing := range existingNames {
			if normalizedName == NormalizeName(existing) {
				continue
			}
			existingSurname, existingGiven, existingValid := SplitJapaneseName(existing)
			if existingValid {
				if givenName == existingGiven {
					sameGiven = append(sameGiven, existing)
				}
				if surname == existingSurname {
					sameSurname = append(sameSurname, existing)
				}
			}
			score := Similarity(name, existing)
			if score >= similarityThreshold {
				highSimilarity = append(highSimilarity, SimilarName{Name: existing, Score: score})
			}
		}
		if len(sameGiven) > 0 {
			issues = append(issues, NameIssue{Type: "same_given_name", Level: "warning", BlocksHire: true, Message: "既存社員と同じ名のため、初期ポリシーでは採用できません", RelatedEmployees: sameGiven})
		}
		if len(sameSurname) > 0 {
			issues = append(issues, NameIssue{Type: "same_surname", Level: "warning", BlocksHire: false, Message: "既存社員と同じ姓です", RelatedEmployees: sameSurname})
		}
		if len(highSimilarity) > 0 {
			issues = append(issues, NameIssue{Type: "high_similarity", Level: "warning", BlocksHire: false, Message: "既存社員と類似度が高い氏名です", RelatedEmployees: highSimilarity})
		}
	}
	result := NameValidation{Name: name, DisplayName: displayName, NormalizedName: normalizedName, Allowed: true, Issues: issues, Errors: []NameIssue{}, Warnings: []NameIssue{}, Reasons: []string{}}
	for _, issue := range issues {
		if issue.Level == "error" {
			result.Errors = append(result.Errors, issue)
		} else if issue.Level == "warning" {
			result.Warnings = append(result.Warnings, issue)
		}
		if issue.BlocksHire {
			result.Allowed = false
			result.Reasons = append(result.Reasons, issue.Message)
		}
	}
	return result
}

type IdentitySummary struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Type   IdentityType `json:"identity_type,omitempty"`
	Source string       `json:"identity_source,omitempty"`
}

type IdentityGroup struct {
	Key       string            `json:"key"`
	Names     []string          `json:"names"`
	Employees []IdentitySummary `json:"employees"`
}

type NamedIdentityGroup struct {
	Key       string
	Value     string
	Names     []string
	Employees []IdentitySummary
}

func (group NamedIdentityGroup) MarshalJSON() ([]byte, error) {
	// Implemented in identity_policy_json.go to keep the public JSON key
	// selected by the group constructor without map-based domain state.
	return marshalNamedIdentityGroup(group)
}

type SimilarName struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

type SimilarityPair struct {
	Employees []IdentitySummary `json:"employees"`
	Names     []string          `json:"names"`
	Score     float64           `json:"score"`
}

type InvalidName struct {
	EmployeeID   string       `json:"employee_id"`
	Name         string       `json:"name"`
	IdentityType IdentityType `json:"identity_type,omitempty"`
	Reasons      []string     `json:"reasons"`
}

type AuditIssue struct {
	Type       string `json:"type"`
	Level      string `json:"level"`
	BlocksHire bool   `json:"blocks_hire"`
	Details    any    `json:"details"`
}

type RepairCandidate struct {
	EmployeeID      string   `json:"employee_id"`
	Name            string   `json:"name"`
	Reasons         []string `json:"reasons"`
	SuggestedAction string   `json:"suggested_action"`
}

type RetainedIdentity struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Type   IdentityType `json:"identity_type,omitempty"`
	Source string       `json:"identity_source,omitempty"`
	Reason string       `json:"reason"`
}

type IdentityAudit struct {
	EmployeeCount                 int                  `json:"employee_count"`
	IdentityCount                 int                  `json:"identity_count"`
	WorkspaceManagerCount         int                  `json:"workspace_manager_count"`
	ReservedIdentityCount         int                  `json:"reserved_identity_count"`
	DuplicateIDs                  []IdentityGroup      `json:"duplicate_ids"`
	ExactMatches                  []IdentityGroup      `json:"exact_matches"`
	NormalizedMatches             []IdentityGroup      `json:"normalized_matches"`
	SameGivenNames                []NamedIdentityGroup `json:"same_given_names"`
	SameSurnames                  []NamedIdentityGroup `json:"same_surnames"`
	HighSimilarityNames           []SimilarityPair     `json:"high_similarity_names"`
	InvalidNames                  []InvalidName        `json:"invalid_names"`
	Issues                        []AuditIssue         `json:"issues"`
	Errors                        []AuditIssue         `json:"errors"`
	Warnings                      []AuditIssue         `json:"warnings"`
	RepairCandidates              []RepairCandidate    `json:"repair_candidates"`
	RecommendedRenameTargets      []RepairCandidate    `json:"recommended_rename_targets"`
	RecommendedRetainedIdentities []RetainedIdentity   `json:"recommended_retained_identities"`
}

func NormalizeDisplayName(name string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(name)), " ")
}

func NormalizeName(name string) string {
	return unicodeCaseFolder.String(strings.Join(strings.Fields(NormalizeDisplayName(name)), ""))
}

func SplitJapaneseName(name string) (string, string, bool) {
	parts := strings.Split(NormalizeDisplayName(name), " ")
	if len(parts) != 2 || !japanesePart(parts[0]) || !japanesePart(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func Similarity(left, right string) float64 {
	leftRunes := []rune(NormalizeName(left))
	rightRunes := []rune(NormalizeName(right))
	if len(leftRunes)+len(rightRunes) == 0 {
		return 1
	}
	matches := sequenceMatches(leftRunes, rightRunes)
	return math.RoundToEven((2*float64(matches)/float64(len(leftRunes)+len(rightRunes)))*1000) / 1000
}

func findInvalidTerms(normalizedName string) []string {
	terms := make([]string, 0)
	for _, term := range invalidTerms {
		if strings.Contains(normalizedName, term) {
			terms = append(terms, term)
		}
	}
	sort.Strings(terms)
	return terms
}

func japanesePart(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'ぁ' && character <= 'ゖ') || (character >= 'ァ' && character <= 'ヺ') ||
			character == 'ー' || (character >= '一' && character <= '龯') ||
			character == '々' || character == '〆' || character == 'ヵ' || character == 'ヶ' {
			continue
		}
		return false
	}
	return true
}

func sequenceMatches(left, right []rune) int {
	type region struct{ leftStart, leftEnd, rightStart, rightEnd int }
	regions := []region{{0, len(left), 0, len(right)}}
	matches := 0
	for len(regions) > 0 {
		current := regions[len(regions)-1]
		regions = regions[:len(regions)-1]
		leftIndex, rightIndex, size := longestMatch(left, right, current)
		if size == 0 {
			continue
		}
		matches += size
		if current.leftStart < leftIndex && current.rightStart < rightIndex {
			regions = append(regions, region{current.leftStart, leftIndex, current.rightStart, rightIndex})
		}
		if leftIndex+size < current.leftEnd && rightIndex+size < current.rightEnd {
			regions = append(regions, region{leftIndex + size, current.leftEnd, rightIndex + size, current.rightEnd})
		}
	}
	return matches
}

func longestMatch(left, right []rune, current struct{ leftStart, leftEnd, rightStart, rightEnd int }) (int, int, int) {
	bestLeft, bestRight, bestSize := current.leftStart, current.rightStart, 0
	previous := make(map[int]int)
	for leftIndex := current.leftStart; leftIndex < current.leftEnd; leftIndex++ {
		next := make(map[int]int)
		for rightIndex := current.rightStart; rightIndex < current.rightEnd; rightIndex++ {
			if left[leftIndex] != right[rightIndex] {
				continue
			}
			size := previous[rightIndex-1] + 1
			next[rightIndex] = size
			candidateLeft := leftIndex - size + 1
			candidateRight := rightIndex - size + 1
			if size > bestSize || (size == bestSize && (candidateLeft < bestLeft || (candidateLeft == bestLeft && candidateRight < bestRight))) {
				bestLeft, bestRight, bestSize = candidateLeft, candidateRight, size
			}
		}
		previous = next
	}
	return bestLeft, bestRight, bestSize
}
