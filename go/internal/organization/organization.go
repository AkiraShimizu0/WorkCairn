// Package organization owns storage-neutral employee and identity rules.
package organization

import "strings"

type IdentityType string

const (
	IdentityEmployee         IdentityType = "employee"
	IdentityWorkspaceManager IdentityType = "workspace_manager"
	IdentityReserved         IdentityType = "reserved"
)

type Identity struct {
	ID          string       `json:"id,omitempty"`
	Name        string       `json:"name,omitempty"`
	Department  string       `json:"department,omitempty"`
	Role        string       `json:"role,omitempty"`
	Model       string       `json:"model,omitempty"`
	Status      string       `json:"status,omitempty"`
	CurrentTask string       `json:"current_task,omitempty"`
	Type        IdentityType `json:"identity_type,omitempty"`
	Source      string       `json:"identity_source,omitempty"`
}

type Inventory struct {
	Employees  []Identity `json:"employees"`
	Managers   []Identity `json:"workspace_managers"`
	Reserved   []Identity `json:"reserved_identities"`
	Identities []Identity `json:"identities"`
}

func NewInventory(employees, managers, reserved []Identity) Inventory {
	normalizedManagers := cloneIdentities(managers)
	for index := range normalizedManagers {
		normalizedManagers[index].Type = IdentityWorkspaceManager
		normalizedManagers[index].Source = "workspace_state"
	}
	normalizedReserved := cloneIdentities(reserved)
	for index := range normalizedReserved {
		normalizedReserved[index].Type = IdentityReserved
		normalizedReserved[index].Source = "organization_reservation"
	}
	result := Inventory{
		Employees: cloneIdentities(employees), Managers: normalizedManagers, Reserved: normalizedReserved,
		Identities: make([]Identity, 0, len(employees)+len(managers)+len(reserved)),
	}
	for _, employee := range employees {
		identity := employee
		identity.Type = IdentityEmployee
		identity.Source = "employee_markdown"
		result.Identities = append(result.Identities, identity)
	}
	for _, manager := range normalizedManagers {
		identity := manager
		result.Identities = append(result.Identities, identity)
	}
	for _, reservedIdentity := range normalizedReserved {
		identity := reservedIdentity
		result.Identities = append(result.Identities, identity)
	}
	return result
}

type ValidationIssue struct {
	Type      string   `json:"type"`
	Name      string   `json:"name,omitempty"`
	Fields    []string `json:"fields,omitempty"`
	ID        string   `json:"id,omitempty"`
	Employees []string `json:"employees,omitempty"`
}

// ValidateInventory preserves the versioned validation ordering: missing
// fields in employee file order, followed by duplicate employee IDs in first
// occurrence order. Managers and reservations participate in Identity policy,
// not employee Markdown structural validation.
func ValidateInventory(inventory Inventory) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	required := []struct {
		name  string
		value func(Identity) string
	}{
		{"id", func(identity Identity) string { return identity.ID }},
		{"department", func(identity Identity) string { return identity.Department }},
		{"role", func(identity Identity) string { return identity.Role }},
		{"model", func(identity Identity) string { return identity.Model }},
		{"status", func(identity Identity) string { return identity.Status }},
	}
	byID := make(map[string][]Identity)
	idOrder := make([]string, 0)
	for _, employee := range inventory.Employees {
		missing := make([]string, 0)
		for _, field := range required {
			if strings.TrimSpace(field.value(employee)) == "" {
				missing = append(missing, field.name)
			}
		}
		if len(missing) > 0 {
			issues = append(issues, ValidationIssue{Type: "missing_fields", Name: employee.Name, Fields: missing})
		}
		if employee.ID != "" {
			if _, exists := byID[employee.ID]; !exists {
				idOrder = append(idOrder, employee.ID)
			}
			byID[employee.ID] = append(byID[employee.ID], employee)
		}
	}
	for _, employeeID := range idOrder {
		matches := byID[employeeID]
		if len(matches) < 2 {
			continue
		}
		names := make([]string, 0, len(matches))
		for _, employee := range matches {
			names = append(names, employee.Name)
		}
		issues = append(issues, ValidationIssue{Type: "duplicate_id", ID: employeeID, Employees: names})
	}
	return issues
}

func cloneIdentities(source []Identity) []Identity {
	if source == nil {
		return []Identity{}
	}
	return append([]Identity(nil), source...)
}
