package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

type EmployeeIDRepairPlan struct {
	Status               string                  `json:"status"`
	Repairs              []organization.IDRepair `json:"repairs"`
	IntentPath           string                  `json:"intent_path,omitempty"`
	ChangedFiles         []string                `json:"changed_files"`
	UnmodifiedReferences []RenameReference       `json:"unmodified_references"`
	Executable           bool                    `json:"executable"`
	ApprovalRequired     bool                    `json:"approval_required"`
	employeeChanges      []employeeRenameChange
	projectionChanges    []employeeRenameChange
}

type EmployeeIDRepairResult struct {
	Status                 string                  `json:"status"`
	Repairs                []organization.IDRepair `json:"repairs"`
	IntentPath             string                  `json:"intent_path,omitempty"`
	IntentCommitted        bool                    `json:"intent_committed"`
	IdentityCommitCount    int                     `json:"identity_commit_count"`
	WorkspaceProjection    bool                    `json:"workspace_projection_committed"`
	ProjectProjectionCount int                     `json:"project_projection_count"`
}

type EmployeeIDRepairError struct {
	Result EmployeeIDRepairResult
	Err    error
}

func (repairError *EmployeeIDRepairError) Error() string {
	return "Employee ID repair partially committed"
}
func (repairError *EmployeeIDRepairError) Unwrap() error { return repairError.Err }

func (store *EmployeeStore) PlanIDRepairs(ctx context.Context, at time.Time) (EmployeeIDRepairPlan, error) {
	if at.IsZero() {
		return EmployeeIDRepairPlan{}, fmt.Errorf("%w: ID repair time", ErrInvalidInput)
	}
	inventory, err := store.loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return EmployeeIDRepairPlan{}, err
	}
	repairs := organization.BuildIDRepairPlan(inventory)
	if len(repairs) == 0 {
		return EmployeeIDRepairPlan{Status: "no_changes", Repairs: []organization.IDRepair{}, ChangedFiles: []string{}, UnmodifiedReferences: []RenameReference{}, Executable: false, ApprovalRequired: false}, nil
	}
	used := make(map[string]struct{}, len(inventory.Identities)+len(repairs))
	for _, identity := range inventory.Identities {
		if identity.ID != "" {
			used[identity.ID] = struct{}{}
		}
	}
	employeeChanges := make([]employeeRenameChange, 0, len(repairs))
	for _, repair := range repairs {
		if err := repair.Validate(); err != nil {
			return EmployeeIDRepairPlan{}, err
		}
		if _, exists := used[repair.ProposedID]; exists {
			return EmployeeIDRepairPlan{}, fmt.Errorf("%w: proposed Employee ID", ErrDuplicateIdentity)
		}
		path := filepath.Join(store.root, "社員", repair.Name+".md")
		original, err := readDocument(path, "Employee Markdown")
		if err != nil {
			return EmployeeIDRepairPlan{}, err
		}
		updated, err := updateEmployeeIDContent(string(original), repair)
		if err != nil {
			return EmployeeIDRepairPlan{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return EmployeeIDRepairPlan{}, err
		}
		employeeChanges = append(employeeChanges, employeeRenameChange{path: path, original: string(original), updated: updated, mode: info.Mode()})
		used[repair.ProposedID] = struct{}{}
	}
	statePath := filepath.Join(store.root, "会社", "Workspace State.md")
	state, err := readDocument(statePath, "Workspace State.md")
	if err != nil {
		return EmployeeIDRepairPlan{}, err
	}
	stateUpdated, err := updateWorkspaceIDs(string(state), repairs)
	if err != nil {
		return EmployeeIDRepairPlan{}, err
	}
	stateInfo, err := os.Stat(statePath)
	if err != nil {
		return EmployeeIDRepairPlan{}, err
	}
	projectChanges, references, err := store.projectIDRepairChanges(repairs)
	if err != nil {
		return EmployeeIDRepairPlan{}, err
	}
	projections := []employeeRenameChange{{path: statePath, original: string(state), updated: stateUpdated, mode: stateInfo.Mode()}}
	projections = append(projections, projectChanges...)
	changed := make([]string, 0, len(employeeChanges)+len(projections))
	for _, change := range append(append([]employeeRenameChange{}, employeeChanges...), projections...) {
		relative, _ := filepath.Rel(store.root, change.path)
		changed = append(changed, filepath.ToSlash(relative))
	}
	intent := filepath.ToSlash(filepath.Join("会社", "Employee ID Repairs", at.In(jstLocation()).Format("20060102-150405")+".json"))
	return EmployeeIDRepairPlan{Status: "ready", Repairs: repairs, IntentPath: intent, ChangedFiles: changed, UnmodifiedReferences: references, Executable: true, ApprovalRequired: true, employeeChanges: employeeChanges, projectionChanges: projections}, nil
}

func (store *EmployeeStore) RepairIDs(ctx context.Context, expected []organization.IDRepair, at time.Time) (EmployeeIDRepairResult, error) {
	release, err := acquireVaultFileLock(ctx, store.lockPath)
	if err != nil {
		return EmployeeIDRepairResult{}, err
	}
	defer func() { _ = release() }()
	plan, err := store.PlanIDRepairs(ctx, at)
	if err != nil {
		return EmployeeIDRepairResult{}, err
	}
	result := EmployeeIDRepairResult{Status: plan.Status, Repairs: plan.Repairs, IntentPath: plan.IntentPath}
	if !reflect.DeepEqual(plan.Repairs, expected) {
		return result, fmt.Errorf("%w: ID repair plan changed", ErrMetadataMismatch)
	}
	if plan.Status == "no_changes" {
		return result, nil
	}
	intentPath := filepath.Join(store.root, filepath.FromSlash(plan.IntentPath))
	if err := os.MkdirAll(filepath.Dir(intentPath), 0o755); err != nil {
		return result, err
	}
	encoded, _ := json.MarshalIndent(struct {
		Schema     int                     `json:"schema_version"`
		Repairs    []organization.IDRepair `json:"repairs"`
		ApprovedAt string                  `json:"approved_at"`
		Changed    []string                `json:"changed_files"`
	}{1, plan.Repairs, at.In(jstLocation()).Format("2006-01-02 15:04:05 MST"), plan.ChangedFiles}, "", "  ")
	encoded = append(encoded, '\n')
	if err := atomicCreateFile(intentPath, encoded, 0o644); err != nil {
		if atomicWriteCommitted(err) {
			result.IntentCommitted = true
			return result, &EmployeeIDRepairError{Result: result, Err: err}
		}
		return result, err
	}
	result.IntentCommitted = true
	for _, change := range plan.employeeChanges {
		if err := store.replacer.Replace(change.path, []byte(change.updated), change.mode); err != nil {
			if atomicWriteCommitted(err) {
				result.IdentityCommitCount++
			}
			return result, &EmployeeIDRepairError{Result: result, Err: err}
		}
		result.IdentityCommitCount++
	}
	for index, change := range plan.projectionChanges {
		if err := store.replacer.Replace(change.path, []byte(change.updated), change.mode); err != nil {
			if atomicWriteCommitted(err) {
				if index == 0 {
					result.WorkspaceProjection = true
				} else {
					result.ProjectProjectionCount++
				}
			}
			return result, &EmployeeIDRepairError{Result: result, Err: err}
		}
		if index == 0 {
			result.WorkspaceProjection = true
		} else {
			result.ProjectProjectionCount++
		}
	}
	result.Status = "repaired"
	return result, nil
}

func atomicWriteCommitted(err error) bool {
	var writeError *AtomicWriteError
	return errors.As(err, &writeError) && writeError.Committed
}

func updateEmployeeIDContent(content string, repair organization.IDRepair) (string, error) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	frontmatter, closed, count := false, false, 0
	for index, line := range lines {
		if line == "---" {
			if !frontmatter && !closed {
				frontmatter = true
			} else if frontmatter {
				frontmatter, closed = false, true
			}
			continue
		}
		if frontmatter && strings.TrimSpace(line) == "id: "+repair.CurrentID {
			count++
			lines[index] = "id: " + repair.ProposedID
		}
		if line == "- ID: "+repair.CurrentID {
			lines[index] = "- ID: " + repair.ProposedID
		}
	}
	if count != 1 || (len(lines) > 0 && lines[0] == "---" && !closed) {
		return "", fmt.Errorf("%w: Employee ID field", ErrInvalidDocument)
	}
	updated := strings.Join(lines, "\n")
	if strings.HasSuffix(content, "\n") {
		updated += "\n"
	}
	return updated, nil
}

func updateWorkspaceIDs(content string, repairs []organization.IDRepair) (string, error) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	counts := make([]int, len(repairs))
	for index, line := range lines {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for cell := range cells {
			cells[cell] = strings.TrimSpace(cells[cell])
		}
		for repairIndex, repair := range repairs {
			if len(cells) > 1 && cells[0] == repair.CurrentID && cells[1] == repair.Name {
				counts[repairIndex]++
				cells[0] = repair.ProposedID
				lines[index] = "| " + strings.Join(cells, " | ") + " |"
			}
		}
	}
	for _, count := range counts {
		if count != 1 {
			return "", ErrMetadataMismatch
		}
	}
	updated := strings.Join(lines, "\n")
	if strings.HasSuffix(content, "\n") {
		updated += "\n"
	}
	return updated, nil
}

func (store *EmployeeStore) projectIDRepairChanges(repairs []organization.IDRepair) ([]employeeRenameChange, []RenameReference, error) {
	root := filepath.Join(store.root, "プロジェクト")
	if info, err := os.Stat(root); errors.Is(err, os.ErrNotExist) || !info.IsDir() {
		return []employeeRenameChange{}, []RenameReference{}, nil
	}
	paths := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".md" {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	sort.Strings(paths)
	changes, references := []employeeRenameChange{}, []RenameReference{}
	for _, path := range paths {
		if historicalRenamePath(path, root) {
			continue
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		original, updated := string(bytes), string(bytes)
		for _, repair := range repairs {
			updated = updateStructuredIDRepair(updated, repair)
		}
		if updated != original {
			info, err := os.Stat(path)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, employeeRenameChange{path: path, original: original, updated: updated, mode: info.Mode()})
		}
		lines := []int{}
		for index, line := range strings.Split(updated, "\n") {
			for _, repair := range repairs {
				if strings.Contains(line, repair.CurrentID) && strings.Contains(line, repair.Name) {
					lines = append(lines, index+1)
					break
				}
			}
		}
		if len(lines) > 0 {
			relative, _ := filepath.Rel(store.root, path)
			references = append(references, RenameReference{Path: filepath.ToSlash(relative), Lines: lines})
		}
	}
	return changes, references, nil
}

func updateStructuredIDRepair(content string, repair organization.IDRepair) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, "|") {
			cells := strings.Split(strings.Trim(line, "|"), "|")
			for cell := range cells {
				cells[cell] = strings.TrimSpace(cells[cell])
			}
			hasID, hasName := false, false
			for _, cell := range cells {
				hasID = hasID || cell == repair.CurrentID
				hasName = hasName || cell == repair.Name
			}
			if hasID && hasName {
				for cell := range cells {
					if cells[cell] == repair.CurrentID {
						cells[cell] = repair.ProposedID
					}
				}
				lines[index] = "| " + strings.Join(cells, " | ") + " |"
			}
		}
	}
	updated := strings.Join(lines, "\n")
	objectPattern := regexp.MustCompile(`(?s)\{[^{}]*\}`)
	updated = objectPattern.ReplaceAllStringFunc(updated, func(block string) string {
		if !regexp.MustCompile(`"(?:name|employee_name|assignee_name|reviewer_name|氏名|担当者名)"\s*:\s*"` + regexp.QuoteMeta(repair.Name) + `"`).MatchString(block) {
			return block
		}
		pattern := regexp.MustCompile(`("(?:id|employee_id|assignee_id|reviewer_id)"\s*:\s*")` + regexp.QuoteMeta(repair.CurrentID) + `(")`)
		return pattern.ReplaceAllString(block, "${1}"+repair.ProposedID+"${2}")
	})
	if strings.HasSuffix(content, "\n") {
		updated += "\n"
	}
	return updated
}
