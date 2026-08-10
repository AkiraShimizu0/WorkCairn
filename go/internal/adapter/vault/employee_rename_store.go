package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

type EmployeeRenamePlan struct {
	Status               string                      `json:"status"`
	Request              organization.RenameRequest  `json:"request"`
	IdentityValidation   organization.NameValidation `json:"identity_validation"`
	IntentPath           string                      `json:"intent_path,omitempty"`
	ChangedFiles         []string                    `json:"changed_files"`
	ExcludedHistorical   []string                    `json:"excluded_historical_records"`
	UnmodifiedReferences []RenameReference           `json:"unmodified_unstructured_references"`
	PostRenameAudit      organization.IdentityAudit  `json:"post_rename_audit"`
	Executable           bool                        `json:"executable"`
	ApprovalRequired     bool                        `json:"approval_required"`
	changes              []employeeRenameChange
}

type EmployeeRenameBatchPlan struct {
	Status             string                        `json:"status"`
	Renames            []organization.RenameRequest  `json:"renames"`
	IdentityValidation []organization.NameValidation `json:"identity_validations"`
	IndividualPlans    []EmployeeRenamePlan          `json:"individual_plans"`
	Executable         bool                          `json:"executable"`
	ApprovalRequired   bool                          `json:"approval_required"`
}

type RenameReference struct {
	Path  string `json:"path"`
	Lines []int  `json:"lines"`
}

type EmployeeRenameResult struct {
	Status                 string `json:"status"`
	EmployeeID             string `json:"employee_id"`
	OldName                string `json:"old_name"`
	NewName                string `json:"new_name"`
	IntentPath             string `json:"intent_path,omitempty"`
	IntentCommitted        bool   `json:"intent_committed"`
	IdentityCommitted      bool   `json:"identity_committed"`
	EmployeeProjection     bool   `json:"employee_projection_committed"`
	WorkspaceProjection    bool   `json:"workspace_projection_committed"`
	ProjectProjectionCount int    `json:"project_projection_count"`
	HistoryCommitted       bool   `json:"history_committed"`
}

type EmployeeRenameError struct {
	Result EmployeeRenameResult
	Err    error
}

func (renameError *EmployeeRenameError) Error() string { return "Employee rename partially committed" }
func (renameError *EmployeeRenameError) Unwrap() error { return renameError.Err }

type employeeRenameChange struct {
	path, original, updated string
	mode                    os.FileMode
}

func (store *EmployeeStore) PlanRename(ctx context.Context, request organization.RenameRequest, at time.Time) (EmployeeRenamePlan, error) {
	request.EmployeeID = strings.TrimSpace(request.EmployeeID)
	request.OldName = strings.TrimSpace(request.OldName)
	request.NewName = strings.TrimSpace(request.NewName)
	request.Reason = strings.TrimSpace(request.Reason)
	if err := request.Validate(); err != nil {
		return EmployeeRenamePlan{}, err
	}
	if at.IsZero() {
		return EmployeeRenamePlan{}, fmt.Errorf("%w: rename time", ErrInvalidInput)
	}
	inventory, err := store.loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return EmployeeRenamePlan{}, err
	}
	employeeMatches, identityMatches := 0, 0
	var employee organization.Identity
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
	oldPath := filepath.Join(store.root, "社員", request.OldName+".md")
	newPath := filepath.Join(store.root, "社員", request.NewName+".md")
	if employeeMatches == 1 && identityMatches == 1 && employee.Name == request.NewName {
		if _, err := os.Lstat(oldPath); errors.Is(err, os.ErrNotExist) {
			return EmployeeRenamePlan{Status: "already_applied", Request: request, ChangedFiles: []string{}, ExcludedHistorical: []string{}, UnmodifiedReferences: []RenameReference{}, Executable: false, ApprovalRequired: false}, nil
		}
	}
	if employeeMatches != 1 || identityMatches != 1 {
		return EmployeeRenamePlan{}, fmt.Errorf("%w: Employee ID is not unique", ErrDuplicateIdentity)
	}
	if employee.Name != request.OldName {
		return EmployeeRenamePlan{}, fmt.Errorf("%w: expected old Employee name", ErrInvalidDocument)
	}
	if _, err := os.Lstat(newPath); err == nil {
		return EmployeeRenamePlan{}, fmt.Errorf("%w: rename destination", ErrAtomicTargetExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return EmployeeRenamePlan{}, err
	}
	names := []string{}
	for _, identity := range inventory.Identities {
		if identity.ID != request.EmployeeID && identity.Name != "" {
			names = append(names, identity.Name)
		}
	}
	validation := organization.ValidateName(request.NewName, names, organization.DefaultSimilarityThreshold)
	if !validation.Allowed {
		return EmployeeRenamePlan{Request: request, IdentityValidation: validation}, fmt.Errorf("%w: new Employee name", ErrDuplicateIdentity)
	}
	content, err := readDocument(oldPath, "Employee Markdown")
	if err != nil {
		return EmployeeRenamePlan{}, err
	}
	updated, err := updateEmployeeRenameContent(string(content), request)
	if err != nil {
		return EmployeeRenamePlan{}, err
	}
	info, err := os.Stat(oldPath)
	if err != nil {
		return EmployeeRenamePlan{}, err
	}
	changes := []employeeRenameChange{{path: newPath, original: string(content), updated: updated, mode: info.Mode()}}
	statePath := filepath.Join(store.root, "会社", "Workspace State.md")
	state, err := readDocument(statePath, "Workspace State.md")
	if err != nil {
		return EmployeeRenamePlan{}, err
	}
	stateUpdated, err := updateWorkspaceRename(string(state), request)
	if err != nil {
		return EmployeeRenamePlan{}, err
	}
	stateInfo, _ := os.Stat(statePath)
	if stateInfo == nil {
		return EmployeeRenamePlan{}, fmt.Errorf("%w: Workspace State stat", ErrInvalidDocument)
	}
	changes = append(changes, employeeRenameChange{path: statePath, original: string(state), updated: stateUpdated, mode: stateInfo.Mode()})
	projectChanges, excluded, unmodified, err := store.projectRenameChanges(request)
	if err != nil {
		return EmployeeRenamePlan{}, err
	}
	changes = append(changes, projectChanges...)
	historyPath := filepath.Join(store.root, "会社", "Identity History.md")
	history, historyMode := "", os.FileMode(0o644)
	if bytes, readErr := os.ReadFile(historyPath); readErr == nil {
		history = string(bytes)
		if statErr := func() error {
			value, e := os.Stat(historyPath)
			if e == nil {
				historyMode = value.Mode()
			}
			return e
		}(); statErr != nil {
			return EmployeeRenamePlan{}, statErr
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return EmployeeRenamePlan{}, readErr
	}
	historyUpdated := renderIdentityHistory(history, request, at)
	changes = append(changes, employeeRenameChange{path: historyPath, original: history, updated: historyUpdated, mode: historyMode})
	intent := filepath.ToSlash(filepath.Join("会社", "Employee Renames", at.In(jstLocation()).Format("20060102-150405")+"-"+request.EmployeeID+".json"))
	postEmployees := append([]organization.Identity(nil), inventory.Employees...)
	postManagers := append([]organization.Identity(nil), inventory.Managers...)
	postReserved := append([]organization.Identity(nil), inventory.Reserved...)
	for index := range postEmployees {
		if postEmployees[index].ID == request.EmployeeID {
			postEmployees[index].Name = request.NewName
		}
	}
	for index := range postManagers {
		if postManagers[index].ID == request.EmployeeID {
			postManagers[index].Name = request.NewName
		}
	}
	for index := range postReserved {
		if postReserved[index].ID == request.EmployeeID {
			postReserved[index].Name = request.NewName
		}
	}
	postAudit := organization.AuditIdentities(organization.NewInventory(postEmployees, postManagers, postReserved), organization.DefaultSimilarityThreshold)
	changed := []string{filepath.ToSlash(filepath.Join("社員", request.NewName+".md")), filepath.ToSlash(filepath.Join("会社", "Workspace State.md"))}
	for _, change := range projectChanges {
		relative, _ := filepath.Rel(store.root, change.path)
		changed = append(changed, filepath.ToSlash(relative))
	}
	changed = append(changed, filepath.ToSlash(filepath.Join("会社", "Identity History.md")))
	return EmployeeRenamePlan{Status: "ready", Request: request, IdentityValidation: validation, IntentPath: intent, ChangedFiles: changed, ExcludedHistorical: excluded, UnmodifiedReferences: unmodified, PostRenameAudit: postAudit, Executable: true, ApprovalRequired: true, changes: changes}, nil
}

func (store *EmployeeStore) PlanRenameBatch(ctx context.Context, requests []organization.RenameRequest, at time.Time) (EmployeeRenameBatchPlan, error) {
	if len(requests) == 0 || at.IsZero() {
		return EmployeeRenameBatchPlan{}, fmt.Errorf("%w: rename batch", ErrInvalidInput)
	}
	for index := range requests {
		requests[index].EmployeeID = strings.TrimSpace(requests[index].EmployeeID)
		requests[index].OldName = strings.TrimSpace(requests[index].OldName)
		requests[index].NewName = strings.TrimSpace(requests[index].NewName)
		requests[index].Reason = strings.TrimSpace(requests[index].Reason)
	}
	inventory, err := store.loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return EmployeeRenameBatchPlan{}, err
	}
	already := 0
	for _, request := range requests {
		matches := 0
		for _, employee := range inventory.Employees {
			if employee.ID == request.EmployeeID && employee.Name == request.NewName {
				matches++
			}
		}
		if matches == 1 {
			oldPath := filepath.Join(store.root, "社員", request.OldName+".md")
			if _, err := os.Lstat(oldPath); errors.Is(err, os.ErrNotExist) {
				already++
			}
		}
	}
	if already > 0 {
		if already != len(requests) {
			return EmployeeRenameBatchPlan{}, fmt.Errorf("%w: partially applied rename batch", ErrMetadataMismatch)
		}
		return EmployeeRenameBatchPlan{Status: "already_applied", Renames: requests, IdentityValidation: []organization.NameValidation{}, IndividualPlans: []EmployeeRenamePlan{}, Executable: false, ApprovalRequired: false}, nil
	}
	validations, err := organization.ValidateRenameBatch(inventory, requests)
	if err != nil {
		return EmployeeRenameBatchPlan{}, err
	}
	plans := make([]EmployeeRenamePlan, 0, len(requests))
	for _, request := range requests {
		plan, err := store.PlanRename(ctx, request, at)
		if err != nil {
			return EmployeeRenameBatchPlan{}, err
		}
		if plan.Status != "ready" {
			return EmployeeRenameBatchPlan{}, fmt.Errorf("%w: rename batch member", ErrMetadataMismatch)
		}
		plans = append(plans, plan)
	}
	return EmployeeRenameBatchPlan{Status: "ready", Renames: requests, IdentityValidation: validations, IndividualPlans: plans, Executable: true, ApprovalRequired: true}, nil
}

func (store *EmployeeStore) Rename(ctx context.Context, request organization.RenameRequest, at time.Time) (EmployeeRenameResult, error) {
	release, err := acquireVaultFileLock(ctx, store.lockPath)
	if err != nil {
		return EmployeeRenameResult{}, err
	}
	defer func() { _ = release() }()
	plan, err := store.PlanRename(ctx, request, at)
	if err != nil {
		return EmployeeRenameResult{}, err
	}
	request = plan.Request
	result := EmployeeRenameResult{Status: plan.Status, EmployeeID: request.EmployeeID, OldName: request.OldName, NewName: request.NewName, IntentPath: plan.IntentPath}
	if plan.Status == "already_applied" {
		return result, nil
	}
	intentPath := filepath.Join(store.root, filepath.FromSlash(plan.IntentPath))
	if err := os.MkdirAll(filepath.Dir(intentPath), 0o755); err != nil {
		return result, err
	}
	intentJSON, _ := json.MarshalIndent(struct {
		Schema     int                        `json:"schema_version"`
		Request    organization.RenameRequest `json:"request"`
		ApprovedAt string                     `json:"approved_at"`
		Changed    []string                   `json:"changed_files"`
		Excluded   []string                   `json:"excluded_historical_records"`
	}{1, request, at.In(jstLocation()).Format("2006-01-02 15:04:05 MST"), plan.ChangedFiles, plan.ExcludedHistorical}, "", "  ")
	intentJSON = append(intentJSON, '\n')
	if err := atomicCreateFile(intentPath, intentJSON, 0o644); err != nil {
		var writeError *AtomicWriteError
		if errors.As(err, &writeError) && writeError.Committed {
			result.IntentCommitted = true
			return result, &EmployeeRenameError{Result: result, Err: err}
		}
		return result, err
	}
	result.IntentCommitted = true
	oldPath := filepath.Join(store.root, "社員", request.OldName+".md")
	newPath := filepath.Join(store.root, "社員", request.NewName+".md")
	if err := os.Rename(oldPath, newPath); err != nil {
		return result, &EmployeeRenameError{Result: result, Err: err}
	}
	result.IdentityCommitted = true
	employeeDirectory, err := os.Open(filepath.Dir(newPath))
	if err != nil {
		return result, &EmployeeRenameError{Result: result, Err: err}
	}
	if err := employeeDirectory.Sync(); err != nil {
		_ = employeeDirectory.Close()
		return result, &EmployeeRenameError{Result: result, Err: err}
	}
	if err := employeeDirectory.Close(); err != nil {
		return result, &EmployeeRenameError{Result: result, Err: err}
	}
	for index, change := range plan.changes {
		if err := store.replacer.Replace(change.path, []byte(change.updated), change.mode); err != nil {
			return result, &EmployeeRenameError{Result: result, Err: err}
		}
		switch {
		case index == 0:
			result.EmployeeProjection = true
		case index == 1:
			result.WorkspaceProjection = true
		case index < len(plan.changes)-1:
			result.ProjectProjectionCount++
		default:
			result.HistoryCommitted = true
		}
	}
	result.Status = "renamed"
	return result, nil
}

func updateEmployeeRenameContent(content string, request organization.RenameRequest) (string, error) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	idCount := 0
	frontmatter := false
	closed := false
	for i, line := range lines {
		if line == "---" {
			if !frontmatter && !closed {
				frontmatter = true
			} else if frontmatter {
				frontmatter = false
				closed = true
			}
			continue
		}
		if strings.TrimSpace(line) == "id: "+request.EmployeeID {
			idCount++
		}
		if frontmatter && strings.HasPrefix(line, "name:") {
			if strings.TrimSpace(strings.TrimPrefix(line, "name:")) != request.OldName {
				return "", ErrInvalidDocument
			}
			lines[i] = "name: " + request.NewName
		}
		if line == "# "+request.OldName {
			lines[i] = "# " + request.NewName
		}
		for _, label := range []string{"- 氏名: ", "- 名前: "} {
			if line == label+request.OldName {
				lines[i] = label + request.NewName
			}
		}
	}
	if idCount != 1 {
		return "", fmt.Errorf("%w: Employee ID", ErrInvalidDocument)
	}
	if len(lines) > 0 && lines[0] == "---" && !closed {
		return "", fmt.Errorf("%w: Employee frontmatter is not closed", ErrInvalidDocument)
	}
	result := strings.Join(lines, "\n")
	if strings.HasSuffix(content, "\n") {
		result += "\n"
	}
	return result, nil
}

func updateWorkspaceRename(content string, request organization.RenameRequest) (string, error) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	matches := 0
	for i, line := range lines {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for j := range cells {
			cells[j] = strings.TrimSpace(cells[j])
		}
		if len(cells) > 1 && cells[0] == request.EmployeeID {
			matches++
			if cells[1] != request.OldName {
				return "", ErrMetadataMismatch
			}
			cells[1] = request.NewName
			lines[i] = "| " + strings.Join(cells, " | ") + " |"
		}
	}
	if matches != 1 {
		return "", ErrMetadataMismatch
	}
	result := strings.Join(lines, "\n")
	if strings.HasSuffix(content, "\n") {
		result += "\n"
	}
	return result, nil
}

func (store *EmployeeStore) projectRenameChanges(request organization.RenameRequest) ([]employeeRenameChange, []string, []RenameReference, error) {
	root := filepath.Join(store.root, "プロジェクト")
	if info, err := os.Stat(root); errors.Is(err, os.ErrNotExist) || !info.IsDir() {
		return []employeeRenameChange{}, []string{}, []RenameReference{}, nil
	}
	paths := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".md" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	sort.Strings(paths)
	changes := []employeeRenameChange{}
	excluded := []string{}
	unmodified := []RenameReference{}
	for _, path := range paths {
		bytes, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, nil, err
		}
		original := string(bytes)
		if !strings.Contains(original, request.OldName) {
			continue
		}
		relative, _ := filepath.Rel(store.root, path)
		if historicalRenamePath(path, root) {
			excluded = append(excluded, filepath.ToSlash(relative))
			continue
		}
		updated := updateStructuredRename(original, request)
		if updated != original {
			info, _ := os.Stat(path)
			changes = append(changes, employeeRenameChange{path: path, original: original, updated: updated, mode: info.Mode()})
		}
		lines := []int{}
		for index, line := range strings.Split(updated, "\n") {
			if strings.Contains(line, request.OldName) {
				lines = append(lines, index+1)
			}
		}
		if len(lines) > 0 {
			unmodified = append(unmodified, RenameReference{Path: filepath.ToSlash(relative), Lines: lines})
		}
	}
	return changes, excluded, unmodified, nil
}

func historicalRenamePath(path, root string) bool {
	relative, _ := filepath.Rel(root, path)
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		for _, directory := range []string{"Backups", "Deliverables", "Reviews", "Revisions"} {
			if part == directory {
				return true
			}
		}
	}
	for _, name := range []string{"Audit Log.md", "Decisions.md", "Progress.md", "Tasks.md"} {
		if filepath.Base(path) == name {
			return true
		}
	}
	return false
}

func updateStructuredRename(content string, request organization.RenameRequest) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "|") {
			cells := strings.Split(strings.Trim(line, "|"), "|")
			for j := range cells {
				cells[j] = strings.TrimSpace(cells[j])
			}
			hasID, hasName := false, false
			for _, cell := range cells {
				hasID = hasID || cell == request.EmployeeID
				hasName = hasName || cell == request.OldName
			}
			if hasID && hasName {
				for j, cell := range cells {
					if cell == request.OldName {
						cells[j] = request.NewName
					}
				}
				lines[i] = "| " + strings.Join(cells, " | ") + " |"
			}
		}
	}
	updated := strings.Join(lines, "\n")
	objectPattern := regexp.MustCompile(`(?s)\{[^{}]*\}`)
	updated = objectPattern.ReplaceAllStringFunc(updated, func(block string) string {
		if !regexp.MustCompile(`"(?:id|employee_id|assignee_id|reviewer_id)"\s*:\s*"` + regexp.QuoteMeta(request.EmployeeID) + `"`).MatchString(block) {
			return block
		}
		pattern := regexp.MustCompile(`("(?:name|employee_name|assignee_name|reviewer_name|氏名|担当者名)"\s*:\s*")` + regexp.QuoteMeta(request.OldName) + `(")`)
		return pattern.ReplaceAllString(block, "${1}"+request.NewName+"${2}")
	})
	updated = updateStructuredRenameBlocks(updated, request)
	if strings.HasSuffix(content, "\n") {
		updated += "\n"
	}
	return updated
}

func updateStructuredRenameBlocks(content string, request organization.RenameRequest) string {
	separator := regexp.MustCompile(`\n\s*\n`)
	ranges := separator.FindAllStringIndex(content, -1)
	var builder strings.Builder
	start := 0
	update := func(block string) string {
		if !regexp.MustCompile(`(?m)^\s*-?\s*(?:id|employee_id|assignee_id|reviewer_id):\s*` + regexp.QuoteMeta(request.EmployeeID) + `\s*$`).MatchString(block) {
			return block
		}
		pattern := regexp.MustCompile(`(?m)^(\s*-?\s*(?:name|employee_name|assignee_name|reviewer_name|氏名|担当者名):\s*)` + regexp.QuoteMeta(request.OldName) + `\s*$`)
		return pattern.ReplaceAllString(block, "${1}"+request.NewName)
	}
	for _, interval := range ranges {
		builder.WriteString(update(content[start:interval[0]]))
		builder.WriteString(content[interval[0]:interval[1]])
		start = interval[1]
	}
	builder.WriteString(update(content[start:]))
	return builder.String()
}

func renderIdentityHistory(content string, request organization.RenameRequest, at time.Time) string {
	stamp := at.In(jstLocation()).Format("2006-01-02 15:04:05 MST")
	base := strings.TrimSpace(content)
	if base == "" {
		base = "---\ntype: identity-history\nupdated_at: " + stamp + "\n---\n\n# Identity History"
	} else {
		lines := strings.Split(base, "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "updated_at:") {
				lines[i] = "updated_at: " + stamp
				break
			}
		}
		base = strings.Join(lines, "\n")
	}
	return base + "\n\n## " + stamp + " Employee Renamed " + request.EmployeeID + "\n\n- employee_id: " + request.EmployeeID + "\n- old_name: " + request.OldName + "\n- new_name: " + request.NewName + "\n- renamed_at: " + stamp + "\n- reason: " + request.Reason + "\n"
}
