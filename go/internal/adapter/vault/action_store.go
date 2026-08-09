package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AkiraShimizu0/workspace-os/go/internal/action"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

const maxActionSourceBytes = 16 << 20

type ActionStore struct {
	projectName string
	projectDir  string
	directory   string
	creator     atomicCreator
}

func NewActionStore(root, projectName string) (*ActionStore, error) {
	root, projectName = strings.TrimSpace(root), strings.TrimSpace(projectName)
	if root == "" || !validPathSegment(projectName) {
		return nil, fmt.Errorf("%w: Vault root and safe Project name are required", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: Vault root", ErrInvalidInput)
	}
	projectDir := filepath.Join(absolute, "プロジェクト", projectName)
	info, err := os.Stat(projectDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: Project directory", ErrDocumentNotFound)
	}
	return &ActionStore{
		projectName: projectName, projectDir: projectDir,
		directory: filepath.Join(projectDir, ".workspace-os", "actions"), creator: osAtomicCreator{},
	}, nil
}

func (store *ActionStore) LoadSource(ctx context.Context, projectID, taskID string) (action.Source, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return action.Source{}, err
	}
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" {
		return action.Source{}, action.ErrInvalidAction
	}
	if _, err := task.ParseTaskID(taskID); err != nil {
		return action.Source{}, action.ErrInvalidAction
	}
	reference := filepath.ToSlash(filepath.Join("Deliverables", taskID+".md"))
	path := filepath.Join(store.projectDir, filepath.FromSlash(reference))
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return action.Source{}, action.ErrNotFound
	}
	if err != nil {
		return action.Source{}, fmt.Errorf("read Action source: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxActionSourceBytes+1))
	if err != nil || len(content) > maxActionSourceBytes {
		return action.Source{}, fmt.Errorf("%w: Action source oversized or unreadable", action.ErrInvalidAction)
	}
	frontmatter, err := parseFrontmatter(string(content))
	if err != nil || strings.TrimSpace(frontmatter["type"]) != "task-deliverable" ||
		strings.TrimSpace(frontmatter["project"]) != store.projectName || strings.TrimSpace(frontmatter["task_id"]) != taskID {
		return action.Source{}, fmt.Errorf("%w: Deliverable identity", action.ErrInvalidAction)
	}
	title, body, err := parseDeliverableBody(string(content))
	if err != nil {
		return action.Source{}, err
	}
	return action.Source{
		ProjectID: projectID, ProjectName: store.projectName, TaskID: taskID,
		Reference: reference, SHA256: action.SourceDigest(content), Title: title, Content: body,
	}, nil
}

func parseDeliverableBody(content string) (string, string, error) {
	if !strings.HasPrefix(content, "---\n") {
		return "", "", action.ErrInvalidAction
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		return "", "", action.ErrInvalidAction
	}
	body := strings.TrimSpace(content[4+end+5:])
	lineEnd := strings.IndexByte(body, '\n')
	if lineEnd < 0 || !strings.HasPrefix(body[:lineEnd], "# ") {
		return "", "", action.ErrInvalidAction
	}
	title := strings.TrimSpace(strings.TrimPrefix(body[:lineEnd], "# "))
	publicationBody := strings.TrimSpace(body[lineEnd+1:])
	if title == "" || strings.ContainsAny(title, "\r\n") || publicationBody == "" {
		return "", "", action.ErrInvalidAction
	}
	return title, publicationBody, nil
}

func (store *ActionStore) SaveIntent(ctx context.Context, intent action.Intent) (action.Evidence, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return action.Evidence{}, err
	}
	if intent.Validate() != nil || intent.Source.ProjectName != store.projectName {
		return action.Evidence{}, action.ErrInvalidAction
	}
	return store.createEvidence(intent.ActionID, "request", intent)
}

func (store *ActionStore) SaveOutcome(ctx context.Context, outcome action.Outcome) (action.Evidence, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return action.Evidence{}, err
	}
	if outcome.Validate() != nil {
		return action.Evidence{}, action.ErrInvalidAction
	}
	intentExists, _, err := store.Exists(ctx, outcome.ActionID)
	if err != nil || !intentExists {
		if err != nil {
			return action.Evidence{}, err
		}
		return action.Evidence{}, action.ErrNotFound
	}
	return store.createEvidence(outcome.ActionID, "result", outcome)
}

func (store *ActionStore) Exists(ctx context.Context, actionID string) (bool, bool, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return false, false, err
	}
	if strings.TrimSpace(actionID) == "" || strings.ContainsAny(actionID, "\r\n") {
		return false, false, action.ErrInvalidAction
	}
	request, err := regularFileExists(store.evidencePath(actionID, "request"))
	if err != nil {
		return false, false, err
	}
	result, err := regularFileExists(store.evidencePath(actionID, "result"))
	return request, result, err
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, action.ErrInvalidAction
	}
	return true, nil
}

func (store *ActionStore) createEvidence(actionID, suffix string, value any) (action.Evidence, error) {
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return action.Evidence{}, err
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return action.Evidence{}, err
	}
	content = append(content, '\n')
	relative := filepath.ToSlash(filepath.Join(".workspace-os", "actions", filepath.Base(store.evidencePath(actionID, suffix))))
	evidence := action.Evidence{RelativePath: relative}
	if err := store.creator.Create(store.evidencePath(actionID, suffix), content, 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			return evidence, action.ErrAlreadyExists
		}
		var writeErr *AtomicWriteError
		if errors.As(err, &writeErr) && writeErr.Committed {
			evidence.Committed = true
			return evidence, &action.SaveError{Evidence: evidence, Err: err}
		}
		return evidence, &action.SaveError{Evidence: evidence, Err: err}
	}
	evidence.Committed = true
	return evidence, nil
}

func (store *ActionStore) evidencePath(actionID, suffix string) string {
	digest := sha256.Sum256([]byte(actionID))
	return filepath.Join(store.directory, hex.EncodeToString(digest[:])+"."+suffix+".json")
}

var _ action.Store = (*ActionStore)(nil)
