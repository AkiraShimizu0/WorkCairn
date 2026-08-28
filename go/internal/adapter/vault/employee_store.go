package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/organization"
)

type EmployeeHireRecord struct {
	Employee            organization.Identity       `json:"employee"`
	RelativePath        string                      `json:"relative_path"`
	CanonicalCommitted  bool                        `json:"canonical_committed"`
	ProjectionCommitted bool                        `json:"projection_committed"`
	IdentityValidation  organization.NameValidation `json:"identity_validation"`
}

type EmployeeHireError struct {
	Record EmployeeHireRecord
	Err    error
}

func (hireError *EmployeeHireError) Error() string { return "Employee hire did not fully commit" }
func (hireError *EmployeeHireError) Unwrap() error { return hireError.Err }

type EmployeeStore struct {
	root     string
	loader   *Loader
	replacer atomicReplacer
	lockPath string
}

type WorkspaceStateSyncPlan struct {
	EmployeeCount   int  `json:"employee_count"`
	DepartmentCount int  `json:"department_count"`
	Executable      bool `json:"executable"`
}

func NewEmployeeStore(root string) (*EmployeeStore, error) {
	loader, err := NewLoader(root)
	if err != nil {
		return nil, err
	}
	return &EmployeeStore{root: loader.root, loader: loader, replacer: osAtomicReplacer{}, lockPath: filepath.Join(loader.root, "会社", ".workspace-os-organization.lock")}, nil
}

func (store *EmployeeStore) PlanHire(ctx context.Context, candidate organization.EmployeeCandidate) (organization.NameValidation, error) {
	if err := candidate.Validate(); err != nil {
		return organization.NameValidation{}, err
	}
	inventory, err := store.loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return organization.NameValidation{}, err
	}
	for _, identity := range inventory.Identities {
		if identity.ID == candidate.ID {
			return organization.NameValidation{}, fmt.Errorf("%w: Employee ID", ErrDuplicateIdentity)
		}
	}
	names := make([]string, 0, len(inventory.Identities))
	for _, identity := range inventory.Identities {
		if identity.Name != "" {
			names = append(names, identity.Name)
		}
	}
	validation := organization.ValidateName(candidate.Name, names, organization.DefaultSimilarityThreshold)
	if !validation.Allowed {
		return validation, fmt.Errorf("%w: Employee name", ErrDuplicateIdentity)
	}
	if _, err := store.workspaceStateContent(); err != nil {
		return validation, err
	}
	return validation, nil
}

func (store *EmployeeStore) PlanWorkspaceStateSync(ctx context.Context) (WorkspaceStateSyncPlan, error) {
	if _, err := store.workspaceStateContent(); err != nil {
		return WorkspaceStateSyncPlan{}, err
	}
	inventory, err := store.loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return WorkspaceStateSyncPlan{}, err
	}
	if issues := organization.ValidateInventory(inventory); len(issues) > 0 {
		return WorkspaceStateSyncPlan{}, fmt.Errorf("%w: invalid Employee inventory", ErrInvalidDocument)
	}
	departments := make(map[string]struct{})
	for _, employee := range inventory.Employees {
		departments[employee.Department] = struct{}{}
	}
	return WorkspaceStateSyncPlan{EmployeeCount: len(inventory.Employees), DepartmentCount: len(departments), Executable: true}, nil
}

func (store *EmployeeStore) SyncWorkspaceState(ctx context.Context, at time.Time) error {
	if at.IsZero() {
		return fmt.Errorf("%w: sync time", ErrInvalidInput)
	}
	release, err := acquireVaultFileLock(ctx, store.lockPath)
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	if _, err := store.PlanWorkspaceStateSync(ctx); err != nil {
		return err
	}
	return store.syncWorkspaceState(ctx, at)
}

func (store *EmployeeStore) Hire(ctx context.Context, candidate organization.EmployeeCandidate, at time.Time) (EmployeeHireRecord, error) {
	release, err := acquireVaultFileLock(ctx, store.lockPath)
	if err != nil {
		return EmployeeHireRecord{}, err
	}
	defer func() { _ = release() }()
	validation, err := store.PlanHire(ctx, candidate)
	if err != nil {
		return EmployeeHireRecord{IdentityValidation: validation}, err
	}
	if at.IsZero() {
		return EmployeeHireRecord{}, fmt.Errorf("%w: hire time", ErrInvalidInput)
	}
	employee := organization.Identity{ID: candidate.ID, Name: candidate.Name, Department: candidate.Department, Role: candidate.Role, Model: candidate.Model, Status: "待機中"}
	record := EmployeeHireRecord{Employee: employee, RelativePath: filepath.ToSlash(filepath.Join("社員", candidate.Name+".md")), IdentityValidation: validation}
	path := filepath.Join(store.root, filepath.FromSlash(record.RelativePath))
	content := renderEmployeeMarkdown(candidate)
	if err := atomicCreateFile(path, []byte(content), 0o644); err != nil {
		var writeError *AtomicWriteError
		if errors.As(err, &writeError) && writeError.Committed {
			record.CanonicalCommitted = true
			return record, &EmployeeHireError{Record: record, Err: err}
		}
		return record, err
	}
	record.CanonicalCommitted = true
	if err := store.syncWorkspaceState(ctx, at); err != nil {
		return record, &EmployeeHireError{Record: record, Err: err}
	}
	record.ProjectionCommitted = true
	return record, nil
}

func renderEmployeeMarkdown(candidate organization.EmployeeCandidate) string {
	return "---\nid: " + candidate.ID + "\ndepartment: " + candidate.Department + "\nrole: " + candidate.Role + "\nmodel: " + candidate.Model + "\nstatus: 待機中\n---\n\n# " + candidate.Name + "\n\n## 基本情報\n\n- ID: " + candidate.ID + "\n- 部署: " + candidate.Department + "\n- 役職: " + candidate.Role + "\n- 使用AI: " + candidate.Model + "\n"
}

func atomicCreateFile(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".workspace-os-employee-*.tmp")
	if err != nil {
		return &AtomicWriteError{Stage: "create_employee_temp", Err: err}
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: Employee file", ErrAtomicTargetExists)
		}
		return &AtomicWriteError{Stage: "publish_employee", Err: err}
	}
	dir, err := os.Open(directory)
	if err != nil {
		return &AtomicWriteError{Stage: "open_employee_directory", Committed: true, Err: err}
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return &AtomicWriteError{Stage: "sync_employee_directory", Committed: true, Err: err}
	}
	if err := dir.Close(); err != nil {
		return &AtomicWriteError{Stage: "close_employee_directory", Committed: true, Err: err}
	}
	return nil
}

func (store *EmployeeStore) workspaceStateContent() (string, error) {
	content, err := readDocument(filepath.Join(store.root, "会社", "Workspace State.md"), "Workspace State.md")
	if err != nil {
		return "", err
	}
	for _, heading := range []string{"Workspace Manager", "部署"} {
		if _, err := markdownSectionLines(string(content), heading); err != nil {
			return "", err
		}
	}
	return string(content), nil
}

func (store *EmployeeStore) syncWorkspaceState(ctx context.Context, at time.Time) error {
	original, err := store.workspaceStateContent()
	if err != nil {
		return err
	}
	inventory, err := store.loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return err
	}
	if issues := organization.ValidateInventory(inventory); len(issues) > 0 {
		return fmt.Errorf("%w: invalid Employee inventory", ErrInvalidDocument)
	}
	current := currentTaskByIdentity(original)
	rows := []string{"| ID | 氏名 | 役割 | 状態 | 現在の作業 |", "|---|---|---|---|---|"}
	managerLines, _ := markdownSectionLines(original, "Workspace Manager")
	for _, line := range managerLines {
		if strings.HasPrefix(line, "| MGR-") {
			rows = append(rows, line)
		}
	}
	employees := append([]organization.Identity(nil), inventory.Employees...)
	sort.Slice(employees, func(i, j int) bool {
		if employees[i].ID != employees[j].ID {
			return employees[i].ID < employees[j].ID
		}
		return employees[i].Name < employees[j].Name
	})
	departments := map[string]int{}
	for _, employee := range employees {
		work := current[employee.ID+"\x00"+employee.Name]
		if work == "" {
			work = "なし"
		}
		rows = append(rows, "| "+employee.ID+" | "+employee.Name+" | "+employee.Role+" | "+employee.Status+" | "+work+" |")
		departments[employee.Department]++
	}
	departmentNames := make([]string, 0, len(departments))
	for name := range departments {
		departmentNames = append(departmentNames, name)
	}
	sort.Strings(departmentNames)
	departmentRows := []string{"| 部署 | 社員数 | 状態 |", "|---|---:|---|"}
	for _, name := range departmentNames {
		departmentRows = append(departmentRows, fmt.Sprintf("| %s | %d | 稼働中 |", name, departments[name]))
	}
	updated, err := replaceMarkdownSection(original, "Workspace Manager", rows)
	if err != nil {
		return err
	}
	updated, err = replaceMarkdownSection(updated, "部署", departmentRows)
	if err != nil {
		return err
	}
	lines := strings.Split(updated, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "updated_at:") {
			lines[i] = "updated_at: " + at.In(jstLocation()).Format("2006-01-02 15:04")
			break
		}
	}
	updated = strings.Join(lines, "\n")
	path := filepath.Join(store.root, "会社", "Workspace State.md")
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return store.replacer.Replace(path, []byte(updated), info.Mode())
}

func currentTaskByIdentity(content string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) == 5 {
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			if cells[0] != "ID" && cells[0] != "---" {
				result[cells[0]+"\x00"+cells[1]] = cells[4]
			}
		}
	}
	return result
}

func replaceMarkdownSection(content, heading string, replacement []string) (string, error) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	target := "## " + heading
	start := -1
	for i, line := range lines {
		if line == target {
			start = i
			break
		}
	}
	if start < 0 {
		return "", ErrInvalidDocument
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	next := append([]string{}, lines[:start+1]...)
	next = append(next, "")
	next = append(next, replacement...)
	next = append(next, "")
	next = append(next, lines[end:]...)
	return strings.TrimRight(strings.Join(next, "\n"), "\n") + "\n", nil
}
