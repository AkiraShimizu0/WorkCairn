package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/organization"
)

// LoadOrganizationInventory reads employee Markdown and the Workspace Manager
// table into storage-neutral identities. Missing/duplicate employee fields are
// returned to Domain validation rather than guessed or repaired here.
func (loader *Loader) LoadOrganizationInventory(ctx context.Context, reserved []organization.Identity) (organization.Inventory, error) {
	if ctx == nil {
		return organization.Inventory{}, fmt.Errorf("%w: context is required", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return organization.Inventory{}, err
	}
	employees, err := loader.loadOrganizationEmployees(ctx)
	if err != nil {
		return organization.Inventory{}, err
	}
	managers, err := loader.loadWorkspaceManagers(ctx)
	if err != nil {
		return organization.Inventory{}, err
	}
	return organization.NewInventory(employees, managers, reserved), nil
}

func (loader *Loader) loadOrganizationEmployees(ctx context.Context) ([]organization.Identity, error) {
	directory := filepath.Join(loader.root, "社員")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("%w: employee directory", ErrDocumentNotFound)
	}
	employees := make([]organization.Identity, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" || entry.Name() == "社員.md" {
			continue
		}
		content, err := readDocument(filepath.Join(directory, entry.Name()), "employee Markdown")
		if err != nil {
			return nil, err
		}
		fields := parseLegacyFrontmatter(content)
		employees = append(employees, organization.Identity{
			ID: strings.TrimSpace(fields["id"]), Name: strings.TrimSuffix(entry.Name(), ".md"),
			Department: strings.TrimSpace(fields["department"]), Role: strings.TrimSpace(fields["role"]),
			Model: strings.TrimSpace(fields["model"]), Status: strings.TrimSpace(fields["status"]),
		})
	}
	return employees, nil
}

func (loader *Loader) loadWorkspaceManagers(ctx context.Context) ([]organization.Identity, error) {
	path := filepath.Join(loader.root, "会社", "Workspace State.md")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []organization.Identity{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: Workspace State.md", ErrDocumentNotFound)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil || len(content) > maxDocumentBytes {
		return nil, fmt.Errorf("%w: Workspace State.md", ErrInvalidDocument)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	section, err := markdownSectionLines(string(content), "Workspace Manager")
	if err != nil {
		return nil, err
	}
	managers := make([]organization.Identity, 0)
	for _, line := range section {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for index := range cells {
			cells[index] = strings.TrimSpace(cells[index])
		}
		if len(cells) < 2 || !strings.HasPrefix(cells[0], "MGR-") {
			continue
		}
		role := "Workspace Manager"
		if len(cells) > 2 {
			role = cells[2]
		}
		manager := organization.Identity{ID: cells[0], Name: cells[1], Role: role}
		if len(cells) > 3 {
			manager.Status = cells[3]
		}
		if len(cells) > 4 {
			manager.CurrentTask = cells[4]
		}
		managers = append(managers, manager)
	}
	return managers, nil
}

func parseLegacyFrontmatter(content []byte) map[string]string {
	fields := make(map[string]string)
	inFrontmatter := false
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "---" {
			inFrontmatter = !inFrontmatter
			continue
		}
		if !inFrontmatter {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if found {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return fields
}

func markdownSectionLines(content, heading string) ([]string, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	target := "## " + heading
	start := -1
	for index, line := range lines {
		if line == target {
			start = index + 1
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("%w: Workspace State section %s", ErrInvalidDocument, heading)
	}
	end := len(lines)
	for index := start; index < len(lines); index++ {
		if strings.HasPrefix(lines[index], "## ") {
			end = index
			break
		}
	}
	return lines[start:end], nil
}
