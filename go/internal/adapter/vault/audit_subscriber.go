package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
)

const (
	maxAuditBytes    = 16 << 20
	auditLockName    = ".workspace-os-audit.lock"
	auditEventPrefix = "<!-- workspace-os-event-id:"
)

var ErrDuplicateAuditEvent = errors.New("Audit Event is already recorded")

// AuditSubscriber is an event.Handler-compatible Vault Adapter. It persists
// immutable Event facts without calling TaskService or interpreting Task state.
type AuditSubscriber struct {
	projectName string
	path        string
	lockPath    string
	replacer    atomicReplacer
}

func NewAuditSubscriber(root, projectName string) (*AuditSubscriber, error) {
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
	return &AuditSubscriber{
		projectName: projectName,
		path:        filepath.Join(projectDirectory, "Audit Log.md"),
		lockPath:    filepath.Join(projectDirectory, auditLockName),
		replacer:    osAtomicReplacer{},
	}, nil
}

func (subscriber *AuditSubscriber) Handle(ctx context.Context, published event.Event) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if err := published.Validate(); err != nil {
		return err
	}
	if strings.ContainsAny(published.ID, "\r\n") || strings.ContainsAny(published.AggregateID, "\r\n") {
		return fmt.Errorf("%w: Event or aggregate ID contains a line break", ErrInvalidInput)
	}
	release, err := acquireVaultFileLock(ctx, subscriber.lockPath)
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	if err := ctx.Err(); err != nil {
		return err
	}

	content, mode, err := subscriber.load()
	if err != nil {
		return err
	}
	marker := auditEventPrefix + published.ID + " -->"
	if strings.Contains(content, marker) {
		return fmt.Errorf("%w: %s", ErrDuplicateAuditEvent, published.ID)
	}
	entry, err := renderAuditEntry(published)
	if err != nil {
		return err
	}
	updated := strings.TrimRight(content, "\r\n") + "\n\n" + entry
	return subscriber.replacer.Replace(subscriber.path, []byte(updated), mode)
}

func (subscriber *AuditSubscriber) load() (string, fs.FileMode, error) {
	file, err := os.Open(subscriber.path)
	if errors.Is(err, os.ErrNotExist) {
		return "---\ntype: audit-log\nproject: " + subscriber.projectName + "\n---\n\n# " + subscriber.projectName + " Audit Log\n", 0o644, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("read Audit Log.md: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxAuditBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("read Audit Log.md: %w", err)
	}
	if len(content) > maxAuditBytes {
		return "", 0, fmt.Errorf("%w: oversized Audit Log.md", ErrInvalidDocument)
	}
	info, err := file.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("stat Audit Log.md: %w", err)
	}
	return string(content), info.Mode(), nil
}

func renderAuditEntry(published event.Event) (string, error) {
	encoded, err := json.MarshalIndent(published, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode Audit Event: %w", err)
	}
	marker := auditEventPrefix + published.ID + " -->"
	return marker + "\n" +
		"## " + published.Timestamp.UTC().Format("2006-01-02 15:04:05Z") + " " + string(published.Type) + " " + published.AggregateID + "\n\n" +
		"```json\n" + string(encoded) + "\n```\n", nil
}

func (subscriber *AuditSubscriber) Handler() event.Handler {
	return subscriber.Handle
}
