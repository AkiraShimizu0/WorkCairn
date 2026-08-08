package organization

import (
	"fmt"
	"strings"
)

type RenameRequest struct {
	EmployeeID string `json:"employee_id"`
	OldName    string `json:"old_name"`
	NewName    string `json:"new_name"`
	Reason     string `json:"reason"`
}

func (request RenameRequest) Validate() error {
	for _, value := range []string{request.EmployeeID, request.OldName, request.NewName, request.Reason} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n|") {
			return fmt.Errorf("invalid Employee rename request")
		}
	}
	if request.OldName == request.NewName {
		return fmt.Errorf("Employee names are unchanged")
	}
	return nil
}

// ValidateRenameBatch validates every target and candidate name before any
// rename write. Target employees are removed from the existing-name set, then
// accepted new names are reserved in request order.
func ValidateRenameBatch(inventory Inventory, requests []RenameRequest) ([]NameValidation, error) {
	if len(requests) == 0 {
		return nil, fmt.Errorf("Employee rename requests are required")
	}
	targets := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		if err := request.Validate(); err != nil {
			return nil, err
		}
		if _, exists := targets[request.EmployeeID]; exists {
			return nil, fmt.Errorf("duplicate Employee rename target: %s", request.EmployeeID)
		}
		targets[request.EmployeeID] = struct{}{}
		employeeMatches, identityMatches := 0, 0
		var employee Identity
		for _, candidate := range inventory.Employees {
			if candidate.ID == request.EmployeeID {
				employeeMatches++
				employee = candidate
			}
		}
		for _, candidate := range inventory.Identities {
			if candidate.ID == request.EmployeeID {
				identityMatches++
			}
		}
		if employeeMatches != 1 || identityMatches != 1 || employee.Name != request.OldName {
			return nil, fmt.Errorf("Employee rename target is not unique or current: %s", request.EmployeeID)
		}
	}
	names := make([]string, 0, len(inventory.Identities)+len(requests))
	for _, identity := range inventory.Identities {
		if _, targeted := targets[identity.ID]; !targeted && identity.Name != "" {
			names = append(names, identity.Name)
		}
	}
	validations := make([]NameValidation, 0, len(requests))
	for _, request := range requests {
		validation := ValidateName(request.NewName, names, DefaultSimilarityThreshold)
		if !validation.Allowed {
			return nil, fmt.Errorf("Employee rename name is not allowed: %s", request.NewName)
		}
		validations = append(validations, validation)
		names = append(names, request.NewName)
	}
	return validations, nil
}
