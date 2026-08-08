package organization

import (
	"fmt"
	"strings"
)

type CandidateValidation struct {
	Candidate EmployeeCandidate `json:"candidate"`
	Identity  NameValidation    `json:"identity_validation"`
}

func ValidateCandidates(inventory Inventory, candidates []EmployeeCandidate) ([]CandidateValidation, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("Employee candidates are required")
	}
	names := make([]string, 0, len(inventory.Identities)+len(candidates))
	usedIDs := make(map[string]struct{}, len(inventory.Identities)+len(candidates))
	for _, identity := range inventory.Identities {
		if identity.Name != "" {
			names = append(names, identity.Name)
		}
		if identity.ID != "" {
			usedIDs[identity.ID] = struct{}{}
		}
	}
	results := make([]CandidateValidation, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.Name) == "" || strings.ContainsAny(candidate.ID+candidate.Name, "\r\n|") {
			return nil, fmt.Errorf("invalid Employee candidate identity")
		}
		if _, exists := usedIDs[candidate.ID]; exists {
			return nil, fmt.Errorf("duplicate Employee candidate ID: %s", candidate.ID)
		}
		validation := ValidateName(candidate.Name, names, DefaultSimilarityThreshold)
		if !validation.Allowed {
			return nil, fmt.Errorf("Employee candidate name is not allowed: %s", candidate.Name)
		}
		results = append(results, CandidateValidation{Candidate: candidate, Identity: validation})
		usedIDs[candidate.ID] = struct{}{}
		names = append(names, candidate.Name)
	}
	return results, nil
}
