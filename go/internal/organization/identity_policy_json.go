package organization

import "encoding/json"

func marshalNamedIdentityGroup(group NamedIdentityGroup) ([]byte, error) {
	return json.Marshal(map[string]any{
		group.Key:   group.Value,
		"names":     group.Names,
		"employees": group.Employees,
	})
}
