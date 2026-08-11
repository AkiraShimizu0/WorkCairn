// Package autonomy defines the user-visible delegation boundary for one
// WorkCairn Workflow. It describes scope; existing approval, Workflow,
// Command Ledger, and Adapter services continue to enforce effects.
package autonomy

import (
	"errors"
	"sort"
	"strings"
)

const SchemaVersion = "workcairn-autonomy.v1"

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
}

func NewStandard(employeeIDs, models []string, executionLimit int) (Contract, error) {
	contract := Contract{
		SchemaVersion: SchemaVersion, TaskExecution: PermissionDelegated, Review: PermissionRequired,
		Revision: PermissionDelegated, ExternalPublish: PermissionSeparateApprove, Spending: PermissionForbidden,
		AllowedEmployeeIDs: canonical(employeeIDs), AllowedModels: canonical(models), ExecutionLimit: executionLimit,
	}
	return contract, contract.Validate()
}

func (contract Contract) Validate() error {
	if contract.SchemaVersion != SchemaVersion || contract.TaskExecution != PermissionDelegated ||
		contract.Review != PermissionRequired || contract.Revision != PermissionDelegated ||
		contract.ExternalPublish != PermissionSeparateApprove || contract.Spending != PermissionForbidden ||
		contract.ExecutionLimit < 1 || contract.ExecutionLimit > 100 ||
		!canonicalList(contract.AllowedEmployeeIDs) || !canonicalList(contract.AllowedModels) {
		return ErrInvalidContract
	}
	return nil
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
