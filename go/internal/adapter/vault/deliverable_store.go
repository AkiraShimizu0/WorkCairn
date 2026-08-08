package vault

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/AkiraShimizu0/workspace-os/go/internal/deliverable"
)

// DeliverableStore persists immutable Python-compatible Task Deliverables in
// one explicitly configured project. It does not mutate Tasks or Audit files.
type DeliverableStore struct {
	projectName string
	directory   string
	creator     atomicCreator
}

type atomicCreator interface {
	Create(path string, content []byte, mode fs.FileMode) error
}

type osAtomicCreator struct{}

func NewDeliverableStore(root, projectName string) (*DeliverableStore, error) {
	root = strings.TrimSpace(root)
	projectName = strings.TrimSpace(projectName)
	if root == "" || !validPathSegment(projectName) {
		return nil, fmt.Errorf("%w: Vault root and safe Project name are required", ErrInvalidInput)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: Vault root is invalid", ErrInvalidInput)
	}
	projectDirectory := filepath.Join(absoluteRoot, "プロジェクト", projectName)
	projectInfo, err := os.Stat(projectDirectory)
	if err != nil || !projectInfo.IsDir() {
		return nil, fmt.Errorf("%w: Project directory", ErrDocumentNotFound)
	}
	return &DeliverableStore{
		projectName: projectName,
		directory:   filepath.Join(projectDirectory, "Deliverables"),
		creator:     osAtomicCreator{},
	}, nil
}

func (store *DeliverableStore) Save(ctx context.Context, document deliverable.Document) (deliverable.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return deliverable.Record{}, err
	}
	if err := document.Validate(); err != nil {
		return deliverable.Record{}, err
	}
	if document.ProjectName != store.projectName {
		return deliverable.Record{}, fmt.Errorf("%w: Project does not match configured Vault directory", deliverable.ErrInvalidDocument)
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return deliverable.Record{}, fmt.Errorf("create Deliverables directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return deliverable.Record{}, err
	}
	relativePath := filepath.ToSlash(filepath.Join("Deliverables", document.Execution.TaskID+".md"))
	target := filepath.Join(store.directory, document.Execution.TaskID+".md")
	content := renderDeliverable(document)
	record := deliverable.Record{TaskID: document.Execution.TaskID, RelativePath: relativePath}
	if err := store.creator.Create(target, []byte(content), 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			return deliverable.Record{}, fmt.Errorf("%w: %s", deliverable.ErrAlreadyExists, filepath.Base(target))
		}
		var writeError *AtomicWriteError
		if errors.As(err, &writeError) && writeError.Committed {
			return record, &deliverable.SaveError{Record: record, Committed: true, Err: err}
		}
		return deliverable.Record{}, &deliverable.SaveError{Err: err}
	}
	return record, nil
}

func renderDeliverable(document deliverable.Document) string {
	executedAt := document.ExecutedAt.In(jstLocation()).Format("2006-01-02 15:04:05")
	return "---\n" +
		"type: task-deliverable\n" +
		"project: " + document.ProjectName + "\n" +
		"task_id: " + document.Execution.TaskID + "\n" +
		"assignee_id: " + document.Execution.EmployeeID + "\n" +
		"runner: " + document.Execution.Runner + "\n" +
		"executed_at: " + executedAt + "\n" +
		"---\n\n" +
		"# " + document.TaskTitle + "\n\n" +
		strings.TrimSpace(document.Execution.Content) + "\n"
}

func (osAtomicCreator) Create(path string, content []byte, mode fs.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".artifact.*.tmp")
	if err != nil {
		return &AtomicWriteError{Stage: "create_temp", Err: err}
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	fail := func(stage string, err error) error {
		_ = temporary.Close()
		return &AtomicWriteError{Stage: stage, Err: err}
	}
	if err := temporary.Chmod(mode.Perm()); err != nil {
		return fail("chmod_temp", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fail("write_temp", err)
	}
	if err := temporary.Sync(); err != nil {
		return fail("sync_temp", err)
	}
	if err := temporary.Close(); err != nil {
		return &AtomicWriteError{Stage: "close_temp", Err: err}
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%w: %s", ErrAtomicTargetExists, filepath.Base(path))
		}
		return &AtomicWriteError{Stage: "publish", Err: err}
	}
	committed = true
	if err := os.Remove(temporaryPath); err != nil {
		return &AtomicWriteError{Stage: "remove_temp", Committed: true, Err: err}
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return &AtomicWriteError{Stage: "open_directory", Committed: true, Err: err}
	}
	if err := directoryHandle.Sync(); err != nil {
		_ = directoryHandle.Close()
		return &AtomicWriteError{Stage: "sync_directory", Committed: true, Err: err}
	}
	if err := directoryHandle.Close(); err != nil {
		return &AtomicWriteError{Stage: "close_directory", Committed: true, Err: err}
	}
	return nil
}

var _ deliverable.Store = (*DeliverableStore)(nil)
