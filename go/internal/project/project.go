package project

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidProject = errors.New("invalid Project")

type Definition struct {
	ID          string `json:"project_id"`
	Name        string `json:"project_name"`
	Description string `json:"description"`
}

func (definition Definition) Validate() error {
	if strings.TrimSpace(definition.ID) == "" || strings.ContainsAny(definition.ID, "\r\n|") {
		return fmt.Errorf("%w: Project ID", ErrInvalidProject)
	}
	if strings.TrimSpace(definition.Name) == "" || strings.ContainsAny(definition.Name, "\r\n|/\\") || definition.Name == "." || definition.Name == ".." {
		return fmt.Errorf("%w: Project name", ErrInvalidProject)
	}
	return nil
}
