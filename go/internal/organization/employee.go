package organization

import (
	"fmt"
	"strings"
)

type EmployeeCandidate struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Department string `json:"department"`
	Role       string `json:"role"`
	Model      string `json:"model"`
}

func (candidate EmployeeCandidate) Validate() error {
	fields := []struct{ name, value string }{{"id", candidate.ID}, {"name", candidate.Name}, {"department", candidate.Department}, {"role", candidate.Role}, {"model", candidate.Model}}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" || strings.ContainsAny(field.value, "\r\n|") {
			return fmt.Errorf("invalid Employee %s", field.name)
		}
	}
	return nil
}
