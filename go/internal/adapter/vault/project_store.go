package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/project"
)

const projectLockFilename = ".workspace-os-projects.lock"

type ProjectRecord struct {
	ProjectID   string            `json:"project_id"`
	ProjectName string            `json:"project_name"`
	Files       map[string]string `json:"files"`
	Committed   bool              `json:"committed"`
}

type ProjectCommitError struct {
	Record ProjectRecord
	Err    error
}

func (commitError *ProjectCommitError) Error() string {
	return "Project bootstrap committed but durability confirmation failed"
}
func (commitError *ProjectCommitError) Unwrap() error { return commitError.Err }

type ProjectStore struct {
	root     string
	projects string
}

func NewProjectStore(root string) (*ProjectStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("%w: Vault root is required", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: Vault root", ErrInvalidInput)
	}
	projects := filepath.Join(absolute, "プロジェクト")
	if info, err := os.Stat(projects); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: projects directory", ErrDocumentNotFound)
	}
	return &ProjectStore{root: absolute, projects: projects}, nil
}

func (store *ProjectStore) Exists(definition project.Definition) (bool, error) {
	definition.ID = strings.TrimSpace(definition.ID)
	definition.Name = strings.TrimSpace(definition.Name)
	if err := definition.Validate(); err != nil {
		return false, err
	}
	_, err := os.Lstat(filepath.Join(store.projects, definition.Name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (store *ProjectStore) Bootstrap(ctx context.Context, definition project.Definition, at time.Time) (ProjectRecord, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return ProjectRecord{}, err
	}
	definition.ID = strings.TrimSpace(definition.ID)
	definition.Name = strings.TrimSpace(definition.Name)
	if err := definition.Validate(); err != nil || at.IsZero() {
		if err != nil {
			return ProjectRecord{}, err
		}
		return ProjectRecord{}, fmt.Errorf("%w: bootstrap time is required", ErrInvalidInput)
	}
	release, err := acquireVaultFileLock(ctx, filepath.Join(store.projects, projectLockFilename))
	if err != nil {
		return ProjectRecord{}, err
	}
	defer func() { _ = release() }()
	target := filepath.Join(store.projects, definition.Name)
	if _, err := os.Lstat(target); err == nil {
		return ProjectRecord{}, fmt.Errorf("%w: Project already exists", ErrAtomicTargetExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ProjectRecord{}, err
	}
	staging, err := os.MkdirTemp(store.projects, ".workspace-os-project-*.tmp")
	if err != nil {
		return ProjectRecord{}, &AtomicWriteError{Stage: "create_project_staging", Err: err}
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()
	timestamp := at.In(jstLocation()).Format("2006-01-02 15:04")
	contents := projectBootstrapContents(definition, timestamp)
	for _, name := range []string{"Project.md", "Tasks.md", "Decisions.md", "Progress.md"} {
		path := filepath.Join(staging, name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return ProjectRecord{}, &AtomicWriteError{Stage: "create_project_file", Err: err}
		}
		if _, err = file.WriteString(contents[name]); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return ProjectRecord{}, &AtomicWriteError{Stage: "write_project_file", Err: err}
		}
	}
	stagingHandle, err := os.Open(staging)
	if err != nil {
		return ProjectRecord{}, &AtomicWriteError{Stage: "open_project_staging", Err: err}
	}
	if err = stagingHandle.Sync(); err == nil {
		err = stagingHandle.Close()
	} else {
		_ = stagingHandle.Close()
	}
	if err != nil {
		return ProjectRecord{}, &AtomicWriteError{Stage: "sync_project_staging", Err: err}
	}
	if _, err := os.Lstat(target); err == nil {
		return ProjectRecord{}, fmt.Errorf("%w: Project appeared during bootstrap", ErrAtomicTargetExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ProjectRecord{}, err
	}
	if err := os.Rename(staging, target); err != nil {
		return ProjectRecord{}, &AtomicWriteError{Stage: "publish_project", Err: err}
	}
	committed = true
	record := ProjectRecord{ProjectID: definition.ID, ProjectName: definition.Name, Files: map[string]string{}, Committed: true}
	for name := range contents {
		record.Files[name] = filepath.ToSlash(filepath.Join("プロジェクト", definition.Name, name))
	}
	parent, err := os.Open(store.projects)
	if err != nil {
		return record, &ProjectCommitError{Record: record, Err: &AtomicWriteError{Stage: "open_projects_directory", Committed: true, Err: err}}
	}
	if err := parent.Sync(); err != nil {
		_ = parent.Close()
		return record, &ProjectCommitError{Record: record, Err: &AtomicWriteError{Stage: "sync_projects_directory", Committed: true, Err: err}}
	}
	if err := parent.Close(); err != nil {
		return record, &ProjectCommitError{Record: record, Err: &AtomicWriteError{Stage: "close_projects_directory", Committed: true, Err: err}}
	}
	return record, nil
}

func projectBootstrapContents(definition project.Definition, timestamp string) map[string]string {
	description := strings.TrimSpace(definition.Description)
	if description == "" {
		description = "未設定"
	}
	projectFrontmatter := "---\ntype: project\nproject_id: " + definition.ID + "\nname: " + definition.Name + "\nstatus: 計画中\ncreated_at: " + timestamp + "\nupdated_at: " + timestamp + "\n---\n\n"
	tasks := "---\ntype: project-tasks\nproject: " + definition.Name + "\nupdated_at: " + timestamp + "\n---\n\n# " + definition.Name + " Tasks\n\n| ID | タスク | 状態 | 担当社員ID | 作成日時 |\n|---|---|---|---|---|\n\n" + taskMetadataMarker + "\n{\n  \"schema_version\": 1,\n  \"tasks\": {}\n}\n-->\n"
	return map[string]string{
		"Project.md":   projectFrontmatter + "# " + definition.Name + "\n\n## 概要\n\n" + description + "\n",
		"Tasks.md":     tasks,
		"Decisions.md": "---\ntype: project-decisions\nproject: " + definition.Name + "\nupdated_at: " + timestamp + "\n---\n\n# " + definition.Name + " Decisions\n\n決定事項はまだありません。\n",
		"Progress.md":  "---\ntype: project-progress\nproject: " + definition.Name + "\nupdated_at: " + timestamp + "\n---\n\n# " + definition.Name + " Progress\n\n進捗記録はまだありません。\n",
	}
}
