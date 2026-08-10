package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/recovery"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

const maxRecoveryDocumentBytes = 16 << 20

var (
	deliverableRecoveryName = regexp.MustCompile(`^(TASK-[0-9]{3,})\.md$`)
	reviewRecoveryName      = regexp.MustCompile(`^(TASK-[0-9]{3,})\.review(?:\.(v[1-9][0-9]*))?\.(json|md)$`)
	revisionRecoveryName    = regexp.MustCompile(`^(TASK-[0-9]{3,})\.revision\.md$`)
	auditRecoveryEntry      = regexp.MustCompile(`(?s)<!-- workspace-os-event-id:[^>]+ -->\s*##[^\n]*\n\s*` + "```json" + `\s*(\{.*?\})\s*` + "```")
)

// RecoverySnapshotReader converts one Vault project into typed evidence. It
// never changes files or infers that a missing Audit entry means an Event was
// not published.
type RecoverySnapshotReader struct {
	root        string
	projectName string
	projectDir  string
	tasks       *TaskStore
}

func NewRecoverySnapshotReader(root, projectName string) (*RecoverySnapshotReader, error) {
	store, err := NewTaskStore(TaskStoreConfig{VaultRoot: root, ProjectName: projectName})
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return nil, fmt.Errorf("%w: Vault root", ErrInvalidInput)
	}
	return &RecoverySnapshotReader{
		root: absolute, projectName: strings.TrimSpace(projectName),
		projectDir: filepath.Join(absolute, "プロジェクト", strings.TrimSpace(projectName)), tasks: store,
	}, nil
}

func (reader *RecoverySnapshotReader) Load(ctx context.Context) (recovery.Snapshot, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return recovery.Snapshot{}, err
	}
	tasks, err := reader.tasks.InspectAll(ctx)
	if err != nil {
		return recovery.Snapshot{}, fmt.Errorf("inspect managed Tasks: %w", err)
	}
	deliverables, err := reader.loadDeliverables(ctx)
	if err != nil {
		return recovery.Snapshot{}, err
	}
	reviews, err := reader.loadReviews(ctx)
	if err != nil {
		return recovery.Snapshot{}, err
	}
	revisions, err := reader.loadRevisions(ctx)
	if err != nil {
		return recovery.Snapshot{}, err
	}
	audit, auditValid, auditProblem, err := reader.loadAudit(ctx)
	if err != nil {
		return recovery.Snapshot{}, err
	}
	commands, err := reader.loadCommands(ctx)
	if err != nil {
		return recovery.Snapshot{}, err
	}
	residuals, err := reader.loadResiduals(ctx)
	if err != nil {
		return recovery.Snapshot{}, err
	}
	return recovery.Snapshot{
		ProjectName: reader.projectName, Tasks: tasks, Deliverables: deliverables,
		Reviews: reviews, Revisions: revisions, Audit: audit,
		AuditValid: auditValid, AuditProblem: auditProblem, Commands: commands, Residuals: residuals,
	}, nil
}

func (reader *RecoverySnapshotReader) loadDeliverables(ctx context.Context) ([]recovery.DeliverableEvidence, error) {
	directory := filepath.Join(reader.projectDir, "Deliverables")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []recovery.DeliverableEvidence{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Deliverables: %w", err)
	}
	result := make([]recovery.DeliverableEvidence, 0, len(entries))
	for _, entry := range entries {
		if err := taskStoreContextError(ctx); err != nil {
			return nil, err
		}
		match := deliverableRecoveryName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		reference := filepath.ToSlash(filepath.Join("Deliverables", entry.Name()))
		evidence := recovery.DeliverableEvidence{TaskID: match[1], Reference: reference}
		path := filepath.Join(directory, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			evidence.Problem = "Deliverable is not a regular file"
			result = append(result, evidence)
			continue
		}
		content, err := readRecoveryDocument(path)
		if err != nil {
			evidence.Problem = "Deliverable cannot be read safely"
			result = append(result, evidence)
			continue
		}
		values, err := parseFrontmatter(string(content))
		if err != nil || values["type"] != "task-deliverable" || values["task_id"] != evidence.TaskID || values["project"] != reader.projectName || strings.TrimSpace(values["assignee_id"]) == "" {
			evidence.Problem = "Deliverable frontmatter does not match its immutable identity"
			result = append(result, evidence)
			continue
		}
		digest := sha256.Sum256(content)
		evidence.Digest = "sha256:" + hex.EncodeToString(digest[:])
		evidence.Project = values["project"]
		evidence.AssigneeID = values["assignee_id"]
		evidence.Valid = true
		result = append(result, evidence)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Reference < result[j].Reference })
	return result, nil
}

func (reader *RecoverySnapshotReader) loadReviews(ctx context.Context) ([]recovery.ReviewEvidence, error) {
	directory := filepath.Join(reader.projectDir, "Reviews")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []recovery.ReviewEvidence{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Reviews: %w", err)
	}
	type pair struct {
		evidence      recovery.ReviewEvidence
		canonicalPath string
	}
	pairs := make(map[string]*pair)
	for _, entry := range entries {
		if err := taskStoreContextError(ctx); err != nil {
			return nil, err
		}
		match := reviewRecoveryName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		key := match[1] + "\x00" + match[2]
		current := pairs[key]
		if current == nil {
			base := match[1] + ".review"
			if match[2] != "" {
				base += "." + match[2]
			}
			current = &pair{evidence: recovery.ReviewEvidence{
				TaskID: match[1], ReviewVersion: match[2],
				CanonicalReference:  filepath.ToSlash(filepath.Join("Reviews", base+".json")),
				ProjectionReference: filepath.ToSlash(filepath.Join("Reviews", base+".md")),
			}}
			pairs[key] = current
		}
		if match[3] == "json" {
			current.evidence.CanonicalExists = true
			current.canonicalPath = filepath.Join(directory, entry.Name())
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				current.evidence.Problem = "canonical Review is not a regular file"
				continue
			}
			content, readErr := readRecoveryDocument(current.canonicalPath)
			if readErr != nil {
				current.evidence.Problem = "canonical Review cannot be read safely"
				continue
			}
			if _, decodeErr := review.DecodeDecision(content); decodeErr != nil {
				current.evidence.Problem = "canonical Review decision is invalid"
				continue
			}
			current.evidence.CanonicalValid = true
		} else {
			current.evidence.ProjectionExists = true
		}
	}
	result := make([]recovery.ReviewEvidence, 0, len(pairs))
	for _, current := range pairs {
		result = append(result, current.evidence)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TaskID != result[j].TaskID {
			return result[i].TaskID < result[j].TaskID
		}
		return result[i].ReviewVersion < result[j].ReviewVersion
	})
	return result, nil
}

func (reader *RecoverySnapshotReader) loadRevisions(ctx context.Context) ([]recovery.RevisionEvidence, error) {
	directory := filepath.Join(reader.projectDir, "Revisions")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []recovery.RevisionEvidence{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Revisions: %w", err)
	}
	result := make([]recovery.RevisionEvidence, 0, len(entries))
	for _, entry := range entries {
		if err := taskStoreContextError(ctx); err != nil {
			return nil, err
		}
		match := revisionRecoveryName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		evidence := recovery.RevisionEvidence{RevisionTaskID: match[1], Reference: filepath.ToSlash(filepath.Join("Revisions", entry.Name()))}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			evidence.Problem = "Revision intent is not a regular file"
			result = append(result, evidence)
			continue
		}
		fields, readErr := readRevisionFrontmatter(filepath.Join(directory, entry.Name()))
		if readErr != nil || fields["type"] != "revision-task" || fields["metadata_version"] != "1" || fields["state"] != "intent_committed" || fields["project"] != reader.projectName || fields["revision_task_id"] != evidence.RevisionTaskID || fields["source_review_canonical"] == "" || fields["assignee_id"] == "" {
			evidence.Problem = "Revision intent metadata is invalid"
			result = append(result, evidence)
			continue
		}
		if _, err := task.ParseTaskID(fields["source_task_id"]); err != nil {
			evidence.Problem = "Revision source Task ID is invalid"
			result = append(result, evidence)
			continue
		}
		evidence.Valid = true
		result = append(result, evidence)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Reference < result[j].Reference })
	return result, nil
}

func (reader *RecoverySnapshotReader) loadAudit(ctx context.Context) ([]recovery.AuditEvidence, bool, string, error) {
	path := filepath.Join(reader.projectDir, "Audit Log.md")
	content, err := readRecoveryDocument(path)
	if errors.Is(err, os.ErrNotExist) {
		return []recovery.AuditEvidence{}, true, "", nil
	}
	if err != nil {
		return nil, false, "", fmt.Errorf("read Audit Log.md: %w", err)
	}
	if err := taskStoreContextError(ctx); err != nil {
		return nil, false, "", err
	}
	matches := auditRecoveryEntry.FindAllSubmatch(content, -1)
	if strings.Count(string(content), auditEventPrefix) != len(matches) {
		return []recovery.AuditEvidence{}, false, "Audit Event marker and JSON entries do not match", nil
	}
	result := make([]recovery.AuditEvidence, 0, len(matches))
	for _, match := range matches {
		var published event.Event
		if err := json.Unmarshal(match[1], &published); err != nil || published.Validate() != nil {
			return []recovery.AuditEvidence{}, false, "Audit contains an invalid Event envelope", nil
		}
		result = append(result, recovery.AuditEvidence{Type: published.Type, AggregateID: published.AggregateID})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].AggregateID != result[j].AggregateID {
			return result[i].AggregateID < result[j].AggregateID
		}
		return result[i].Type < result[j].Type
	})
	return result, true, "", nil
}

func (reader *RecoverySnapshotReader) loadCommands(ctx context.Context) ([]recovery.CommandEvidence, error) {
	projectCommands, err := reader.loadCommandDirectory(
		ctx,
		filepath.Join(reader.projectDir, ".workspace-os", "commands"),
		filepath.Join(".workspace-os", "commands"),
		reader.projectName,
	)
	if err != nil {
		return nil, err
	}
	workspaceCommands, err := reader.loadCommandDirectory(
		ctx,
		filepath.Join(reader.root, ".workspace-os", "commands"),
		filepath.Join("workspace", ".workspace-os", "commands"),
		"workspace",
	)
	if err != nil {
		return nil, err
	}
	result := append(projectCommands, workspaceCommands...)
	sort.Slice(result, func(i, j int) bool { return result[i].Reference < result[j].Reference })
	return result, nil
}

func (reader *RecoverySnapshotReader) loadCommandDirectory(ctx context.Context, directory, referenceDirectory, expectedScope string) ([]recovery.CommandEvidence, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []recovery.CommandEvidence{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Command Ledger: %w", err)
	}
	result := make([]recovery.CommandEvidence, 0, len(entries))
	for _, entry := range entries {
		if err := taskStoreContextError(ctx); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		reference := filepath.ToSlash(filepath.Join(referenceDirectory, entry.Name()))
		evidence := recovery.CommandEvidence{Reference: reference}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			evidence.Problem = "Command Ledger record is not a regular file"
			result = append(result, evidence)
			continue
		}
		content, readErr := readRecoveryDocument(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			evidence.Problem = "Command Ledger record cannot be read safely"
			result = append(result, evidence)
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(string(content)))
		decoder.DisallowUnknownFields()
		var record commandledger.Record
		decodeErr := decoder.Decode(&record)
		trailingErr := decoder.Decode(&struct{}{})
		evidence.CommandID = record.CommandID
		evidence.Operation = record.Operation
		evidence.AggregateID = record.AggregateID
		evidence.State = record.State
		digest := sha256.Sum256([]byte(record.CommandID))
		expectedName := hex.EncodeToString(digest[:]) + ".json"
		if decodeErr != nil || !errors.Is(trailingErr, io.EOF) || record.Validate() != nil || entry.Name() != expectedName || record.ProjectName != expectedScope {
			evidence.Problem = "Command Ledger record is invalid or stored under the wrong identity"
			result = append(result, evidence)
			continue
		}
		evidence.Valid = true
		result = append(result, evidence)
	}
	return result, nil
}

func (reader *RecoverySnapshotReader) loadResiduals(ctx context.Context) ([]recovery.ResidualEvidence, error) {
	result := make([]recovery.ResidualEvidence, 0)
	err := filepath.WalkDir(reader.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := taskStoreContextError(ctx); err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".obsidian") {
			return filepath.SkipDir
		}
		kind := residualKind(entry.Name(), entry.IsDir())
		if kind == "" {
			return nil
		}
		reference, err := filepath.Rel(reader.root, path)
		if err != nil {
			return err
		}
		result = append(result, recovery.ResidualEvidence{Reference: filepath.ToSlash(reference), Kind: kind})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan residual temporary state: %w", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Reference < result[j].Reference })
	return result, nil
}

func residualKind(name string, directory bool) string {
	patterns := []struct {
		pattern string
		kind    string
		dir     bool
	}{
		{".workspace-os-project-*.tmp", "project_staging_directory", true},
		{".artifact.*.tmp", "artifact_temporary_file", false},
		{".workspace-os.*.tmp", "atomic_replacement_temporary_file", false},
		{".workspace-os-employee-*.tmp", "employee_temporary_file", false},
	}
	for _, candidate := range patterns {
		matched, _ := filepath.Match(candidate.pattern, name)
		if matched && candidate.dir == directory {
			return candidate.kind
		}
	}
	return ""
}

func readRecoveryDocument(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxRecoveryDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxRecoveryDocumentBytes {
		return nil, fmt.Errorf("%w: recovery evidence is oversized", ErrInvalidDocument)
	}
	return content, nil
}

var _ recovery.SnapshotReader = (*RecoverySnapshotReader)(nil)
