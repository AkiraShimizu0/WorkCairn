package organization

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// IDRepair identifies one employee by the otherwise durable name/file
// identity while repairing an accidentally duplicated Employee ID.
type IDRepair struct {
	Name       string `json:"name"`
	CurrentID  string `json:"current_id"`
	ProposedID string `json:"proposed_id"`
}

func (repair IDRepair) Validate() error {
	for _, value := range []string{repair.Name, repair.CurrentID, repair.ProposedID} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n|") {
			return fmt.Errorf("invalid Employee ID repair")
		}
	}
	if repair.CurrentID == repair.ProposedID {
		return fmt.Errorf("Employee IDs are unchanged")
	}
	return nil
}

// BuildIDRepairPlan preserves Python's deterministic rule: employee filename
// order decides which duplicate keeps its ID, and later employees receive the
// next available numeric suffix. All identity types reserve IDs.
func BuildIDRepairPlan(inventory Inventory) []IDRepair {
	employees := append([]Identity(nil), inventory.Employees...)
	sort.SliceStable(employees, func(i, j int) bool { return employees[i].Name < employees[j].Name })
	used := make(map[string]struct{}, len(inventory.Identities))
	for _, identity := range inventory.Identities {
		if identity.ID != "" {
			used[identity.ID] = struct{}{}
		}
	}
	groups := make(map[string][]Identity)
	order := make([]string, 0)
	for _, employee := range employees {
		if employee.ID == "" {
			continue
		}
		if _, exists := groups[employee.ID]; !exists {
			order = append(order, employee.ID)
		}
		groups[employee.ID] = append(groups[employee.ID], employee)
	}
	result := make([]IDRepair, 0)
	for _, duplicateID := range order {
		matches := groups[duplicateID]
		if len(matches) < 2 {
			continue
		}
		prefix, digits := repairIDParts(duplicateID)
		next, _ := strconv.Atoi(digits)
		next++
		width := len(digits)
		if width < 3 {
			width = 3
		}
		for _, employee := range matches[1:] {
			var proposed string
			for {
				proposed = fmt.Sprintf("%s-%0*d", prefix, width, next)
				next++
				if _, exists := used[proposed]; !exists {
					break
				}
			}
			used[proposed] = struct{}{}
			result = append(result, IDRepair{Name: employee.Name, CurrentID: duplicateID, ProposedID: proposed})
		}
	}
	return result
}

func repairIDParts(value string) (string, string) {
	index := strings.LastIndex(value, "-")
	if index < 0 || index == len(value)-1 {
		return value, "0"
	}
	digits := value[index+1:]
	if _, err := strconv.Atoi(digits); err != nil {
		return value, "0"
	}
	return value[:index], digits
}
