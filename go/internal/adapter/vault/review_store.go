package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/review"
)

// ReviewStore commits immutable structured evidence before its human-readable
// projection according to ADR-0010. It never mutates Tasks or Audit files.
type ReviewStore struct {
	projectName string
	directory   string
	creator     atomicCreator
}

func NewReviewStore(root, projectName string) (*ReviewStore, error) {
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
	return &ReviewStore{
		projectName: projectName,
		directory:   filepath.Join(projectDirectory, "Reviews"),
		creator:     osAtomicCreator{},
	}, nil
}

func (store *ReviewStore) Save(ctx context.Context, document review.Document) (review.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return review.Record{}, err
	}
	if err := document.Validate(); err != nil {
		return review.Record{}, err
	}
	if document.ProjectName != store.projectName {
		return review.Record{}, fmt.Errorf("%w: Project does not match configured Vault directory", review.ErrInvalidDocument)
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return review.Record{}, fmt.Errorf("create Reviews directory: %w", err)
	}

	baseName := document.Execution.TaskID + ".review"
	if document.ReviewVersion != "" {
		baseName += "." + document.ReviewVersion
	}
	canonicalName := baseName + ".json"
	projectionName := baseName + ".md"
	record := review.Record{
		TaskID:         document.Execution.TaskID,
		ReviewVersion:  document.ReviewVersion,
		CanonicalPath:  filepath.ToSlash(filepath.Join("Reviews", canonicalName)),
		ProjectionPath: filepath.ToSlash(filepath.Join("Reviews", projectionName)),
	}
	canonicalTarget := filepath.Join(store.directory, canonicalName)
	projectionTarget := filepath.Join(store.directory, projectionName)
	for _, target := range []string{canonicalTarget, projectionTarget} {
		if _, err := os.Lstat(target); err == nil {
			return record, &review.SaveError{Record: record, Stage: "preflight", Err: fmt.Errorf("%w: %s", review.ErrAlreadyExists, filepath.Base(target))}
		} else if !errors.Is(err, os.ErrNotExist) {
			return record, &review.SaveError{Record: record, Stage: "preflight", Err: err}
		}
	}
	if err := ctx.Err(); err != nil {
		return record, err
	}

	canonicalContent, err := json.MarshalIndent(document.Execution.Decision, "", "  ")
	if err != nil {
		return record, &review.SaveError{Record: record, Stage: "canonical_encode", Err: err}
	}
	canonicalContent = append(canonicalContent, '\n')
	if err := store.creator.Create(canonicalTarget, canonicalContent, 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			err = fmt.Errorf("%w: %s", review.ErrAlreadyExists, canonicalName)
		}
		var writeError *AtomicWriteError
		if errors.As(err, &writeError) && writeError.Committed {
			record.CanonicalCommitted = true
		}
		return record, &review.SaveError{Record: record, Stage: "canonical", Err: err}
	}
	record.CanonicalCommitted = true
	if err := ctx.Err(); err != nil {
		return record, &review.SaveError{Record: record, Stage: "projection", Err: err}
	}

	projectionContent := renderReviewProjection(document, canonicalName)
	if err := store.creator.Create(projectionTarget, []byte(projectionContent), 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			err = fmt.Errorf("%w: %s", review.ErrAlreadyExists, projectionName)
		}
		var writeError *AtomicWriteError
		if errors.As(err, &writeError) && writeError.Committed {
			record.ProjectionCommitted = true
		}
		return record, &review.SaveError{Record: record, Stage: "projection", Err: err}
	}
	record.ProjectionCommitted = true
	return record, nil
}

func renderReviewProjection(document review.Document, resultFile string) string {
	timestamp := document.ReviewedAt.In(jstLocation()).Format("2006-01-02 15:04:05 MST")
	versionLine := ""
	if document.ReviewVersion != "" {
		versionLine = "version: " + document.ReviewVersion + "\n"
	}
	return "---\n" +
		"type: review\n" +
		"project: " + document.ProjectName + "\n" +
		"task_id: " + document.Execution.TaskID + "\n" +
		"reviewer: " + document.Execution.ReviewerID + "\n" +
		"reviewed_at: " + timestamp + "\n" +
		"runner: " + document.Execution.Runner + "\n" +
		"model: " + document.Execution.Model + "\n" +
		versionLine +
		"result_file: " + resultFile + "\n" +
		"---\n\n" +
		"# " + document.Execution.TaskID + " Review\n\n" +
		strings.TrimSpace(document.Execution.HumanMarkdown) + "\n\n" +
		"## 判定\n\n" +
		string(document.Execution.Decision.Verdict) + "\n"
}

var _ review.Store = (*ReviewStore)(nil)
