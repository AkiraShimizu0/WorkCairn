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
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

const (
	taskMetadataMarker       = "<!-- workspace-os-task-metadata:v1"
	taskMetadataMarkerPrefix = "<!-- workspace-os-task-metadata:v"
	taskMetadataSchema       = 1
	taskLockFilename         = ".workspace-os-tasks.lock"
)

var taskTableHeader = []string{"ID", "タスク", "状態", "担当社員ID", "作成日時"}

// TaskStoreConfig identifies one project-scoped Tasks.md. The clock is used
// only for the human-readable creation timestamp of TaskStore.Create.
type TaskStoreConfig struct {
	VaultRoot   string
	ProjectName string
	Clock       func() time.Time
}

// TaskStore persists Task Domain state in the current five-column Tasks.md
// plus the ADR-0008 managed metadata block.
type TaskStore struct {
	path     string
	lockPath string
	clock    func() time.Time
	replacer atomicReplacer
}

type atomicReplacer interface {
	Replace(path string, content []byte, mode fs.FileMode) error
}

type osAtomicReplacer struct{}

type taskMetadataDocument struct {
	SchemaVersion int                     `json:"schema_version"`
	Tasks         map[string]taskMetadata `json:"tasks"`
}

type taskMetadata struct {
	Version           uint64 `json:"version"`
	LastFailureReason string `json:"last_failure_reason,omitempty"`
	HoldReason        string `json:"hold_reason,omitempty"`
	TableDigest       string `json:"table_digest"`
}

type taskTableRow struct {
	Task      task.Task
	CreatedAt string
	LineIndex int
}

type managedTaskDocument struct {
	lines           []string
	newline         string
	trailingNewline bool
	tableEnd        int
	originalRows    int
	metadataStart   int
	metadataEnd     int
	rows            []taskTableRow
	metadata        taskMetadataDocument
}

func NewTaskStore(config TaskStoreConfig) (*TaskStore, error) {
	root := strings.TrimSpace(config.VaultRoot)
	projectName := strings.TrimSpace(config.ProjectName)
	if root == "" || !validPathSegment(projectName) {
		return nil, fmt.Errorf("%w: Vault root and safe Project name are required", ErrInvalidInput)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: Vault root is invalid", ErrInvalidInput)
	}
	rootInfo, err := os.Stat(absoluteRoot)
	if err != nil || !rootInfo.IsDir() {
		return nil, fmt.Errorf("%w: Vault root", ErrDocumentNotFound)
	}
	projectDirectory := filepath.Join(absoluteRoot, "プロジェクト", projectName)
	projectInfo, err := os.Stat(projectDirectory)
	if err != nil || !projectInfo.IsDir() {
		return nil, fmt.Errorf("%w: Project directory", ErrDocumentNotFound)
	}
	tasksPath := filepath.Join(projectDirectory, "Tasks.md")
	if tasksInfo, err := os.Stat(tasksPath); err != nil || !tasksInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: Tasks.md", ErrDocumentNotFound)
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &TaskStore{
		path:     tasksPath,
		lockPath: filepath.Join(projectDirectory, taskLockFilename),
		clock:    clock,
		replacer: osAtomicReplacer{},
	}, nil
}

func (store *TaskStore) Create(ctx context.Context, created task.Task) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if err := created.Validate(); err != nil {
		return err
	}
	if err := validatePersistedTask(created); err != nil {
		return err
	}
	if created.Version != 1 {
		return task.ErrInvalidVersion
	}
	if err := store.preflightManagedDocument(); err != nil {
		return err
	}
	return store.withExclusiveLock(ctx, func() error {
		document, mode, err := store.load()
		if err != nil {
			return err
		}
		if _, exists := document.metadata.Tasks[created.ID]; exists {
			return fmt.Errorf("%w: %s", task.ErrTaskAlreadyExists, created.ID)
		}
		createdAt := store.clock().In(jstLocation()).Format("2006-01-02 15:04")
		row := taskTableRow{Task: created.Clone(), CreatedAt: createdAt}
		document.rows = append(document.rows, row)
		document.metadata.Tasks[created.ID] = metadataForRow(row)
		return store.persist(document, mode)
	})
}

func (store *TaskStore) Get(ctx context.Context, taskID string) (task.Task, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return task.Task{}, err
	}
	taskID = strings.TrimSpace(taskID)
	if _, err := task.ParseTaskID(taskID); err != nil {
		return task.Task{}, err
	}
	if err := store.preflightManagedDocument(); err != nil {
		return task.Task{}, err
	}
	var found task.Task
	err := store.withExclusiveLock(ctx, func() error {
		document, _, err := store.load()
		if err != nil {
			return err
		}
		for _, row := range document.rows {
			if row.Task.ID == taskID {
				found = row.Task.Clone()
				return nil
			}
		}
		return fmt.Errorf("%w: %s", task.ErrTaskNotFound, taskID)
	})
	return found, err
}

// Inspect reads one atomic Tasks.md snapshot without creating a lock file.
// Writers publish by atomic replacement and enforce CAS under an exclusive
// lock, so a read-only plan sees either the old or the new complete revision.
func (store *TaskStore) Inspect(ctx context.Context, taskID string) (task.Task, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return task.Task{}, err
	}
	taskID = strings.TrimSpace(taskID)
	if _, err := task.ParseTaskID(taskID); err != nil {
		return task.Task{}, err
	}
	content, err := readDocument(store.path, "Tasks.md")
	if err != nil {
		return task.Task{}, err
	}
	document, err := parseManagedTaskDocument(string(content))
	if err != nil {
		return task.Task{}, err
	}
	for _, row := range document.rows {
		if row.Task.ID == taskID {
			return row.Task.Clone(), nil
		}
	}
	return task.Task{}, fmt.Errorf("%w: %s", task.ErrTaskNotFound, taskID)
}

// InspectAll reads one complete managed Tasks.md snapshot without creating a
// lock file. It is intended for read-only planning such as deterministic Task
// ID allocation; mutations must still go through TaskService.
func (store *TaskStore) InspectAll(ctx context.Context) ([]task.Task, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return nil, err
	}
	content, err := readDocument(store.path, "Tasks.md")
	if err != nil {
		return nil, err
	}
	document, err := parseManagedTaskDocument(string(content))
	if err != nil {
		return nil, err
	}
	tasks := make([]task.Task, 0, len(document.rows))
	for _, row := range document.rows {
		tasks = append(tasks, row.Task.Clone())
	}
	return tasks, nil
}

func (store *TaskStore) Update(ctx context.Context, next task.Task, expectedVersion uint64) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := validatePersistedTask(next); err != nil {
		return err
	}
	if err := store.preflightManagedDocument(); err != nil {
		return err
	}
	return store.withExclusiveLock(ctx, func() error {
		document, mode, err := store.load()
		if err != nil {
			return err
		}
		rowIndex := -1
		for index, row := range document.rows {
			if row.Task.ID == next.ID {
				rowIndex = index
				break
			}
		}
		if rowIndex == -1 {
			return fmt.Errorf("%w: %s", task.ErrTaskNotFound, next.ID)
		}
		current := document.rows[rowIndex].Task
		if current.Version != expectedVersion || next.Version != expectedVersion+1 {
			return fmt.Errorf(
				"%w: %s expected=%d actual=%d next=%d",
				task.ErrVersionConflict,
				next.ID,
				expectedVersion,
				current.Version,
				next.Version,
			)
		}
		updatedRow := document.rows[rowIndex]
		updatedRow.Task = next.Clone()
		document.rows[rowIndex] = updatedRow
		document.metadata.Tasks[next.ID] = metadataForRow(updatedRow)
		return store.persist(document, mode)
	})
}

func (store *TaskStore) withExclusiveLock(ctx context.Context, operation func() error) error {
	release, err := acquireVaultFileLock(ctx, store.lockPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = release()
	}()
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	return operation()
}

func (store *TaskStore) load() (managedTaskDocument, fs.FileMode, error) {
	content, err := readDocument(store.path, "Tasks.md")
	if err != nil {
		return managedTaskDocument{}, 0, err
	}
	info, err := os.Stat(store.path)
	if err != nil {
		return managedTaskDocument{}, 0, fmt.Errorf("%w: Tasks.md stat", ErrInvalidDocument)
	}
	document, err := parseManagedTaskDocument(string(content))
	if err != nil {
		return managedTaskDocument{}, 0, err
	}
	return document, info.Mode(), nil
}

func (store *TaskStore) preflightManagedDocument() error {
	_, _, err := store.load()
	return err
}

func (store *TaskStore) persist(document managedTaskDocument, mode fs.FileMode) error {
	content, err := document.render()
	if err != nil {
		return err
	}
	return store.replacer.Replace(store.path, []byte(content), mode)
}

func parseManagedTaskDocument(content string) (managedTaskDocument, error) {
	lines, newline, trailingNewline, err := splitTaskDocument(content)
	if err != nil {
		return managedTaskDocument{}, err
	}
	rows, tableEnd, err := parseTaskTableRows(lines)
	if err != nil {
		return managedTaskDocument{}, err
	}
	metadata, metadataStart, metadataEnd, err := parseTaskMetadata(lines, tableEnd)
	if err != nil {
		return managedTaskDocument{}, err
	}
	if err := combineTaskRows(rows, metadata); err != nil {
		return managedTaskDocument{}, err
	}
	return managedTaskDocument{
		lines:           lines,
		newline:         newline,
		trailingNewline: trailingNewline,
		tableEnd:        tableEnd,
		originalRows:    len(rows),
		metadataStart:   metadataStart,
		metadataEnd:     metadataEnd,
		rows:            rows,
		metadata:        metadata,
	}, nil
}

func splitTaskDocument(content string) ([]string, string, bool, error) {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
		content = strings.ReplaceAll(content, "\r\n", "\n")
	}
	if strings.ContainsRune(content, '\r') {
		return nil, "", false, fmt.Errorf("%w: unsupported line endings", ErrInvalidDocument)
	}
	trailingNewline := strings.HasSuffix(content, "\n")
	if trailingNewline {
		content = strings.TrimSuffix(content, "\n")
	}
	return strings.Split(content, "\n"), newline, trailingNewline, nil
}

func parseTaskTableRows(lines []string) ([]taskTableRow, int, error) {
	headerIndex := -1
	for index, line := range lines {
		cells, ok := tableCells(line)
		if !ok || !sameCells(cells, taskTableHeader) {
			continue
		}
		if headerIndex != -1 {
			return nil, 0, fmt.Errorf("%w: duplicate Tasks table", ErrInvalidDocument)
		}
		headerIndex = index
	}
	if headerIndex == -1 || headerIndex+1 >= len(lines) {
		return nil, 0, fmt.Errorf("%w: Tasks table header", ErrInvalidDocument)
	}
	separator, ok := tableCells(lines[headerIndex+1])
	if !ok || len(separator) != 5 {
		return nil, 0, fmt.Errorf("%w: Tasks table separator", ErrInvalidDocument)
	}
	for _, cell := range separator {
		if cell != "---" {
			return nil, 0, fmt.Errorf("%w: Tasks table separator", ErrInvalidDocument)
		}
	}

	rows := make([]taskTableRow, 0)
	seen := make(map[string]struct{})
	tableEnd := headerIndex + 1
	for index := headerIndex + 2; index < len(lines); index++ {
		cells, isTableLine := tableCells(lines[index])
		if !isTableLine {
			break
		}
		if len(cells) != 5 {
			return nil, 0, fmt.Errorf("%w: Tasks table row", ErrInvalidDocument)
		}
		parsed, err := taskRowFromCells(cells, index)
		if err != nil {
			return nil, 0, err
		}
		if _, exists := seen[parsed.Task.ID]; exists {
			return nil, 0, fmt.Errorf("%w: duplicate Task ID %s", ErrMetadataDuplicate, parsed.Task.ID)
		}
		seen[parsed.Task.ID] = struct{}{}
		rows = append(rows, parsed)
		tableEnd = index
	}
	return rows, tableEnd, nil
}

func taskRowFromCells(cells []string, lineIndex int) (taskTableRow, error) {
	status, err := task.ParseStatus(cells[2])
	if err != nil {
		return taskTableRow{}, fmt.Errorf("%w: Task status", ErrInvalidDocument)
	}
	var assigneeID *string
	if cells[3] != "" && cells[3] != "未割当" {
		value := cells[3]
		assigneeID = &value
	}
	parsed := task.Task{
		ID:         cells[0],
		Title:      cells[1],
		AssigneeID: assigneeID,
		Status:     status,
		Version:    1,
	}
	if err := parsed.Validate(); err != nil {
		return taskTableRow{}, fmt.Errorf("%w: Task fields", ErrInvalidDocument)
	}
	if strings.TrimSpace(cells[4]) == "" {
		return taskTableRow{}, fmt.Errorf("%w: Task creation time", ErrInvalidDocument)
	}
	return taskTableRow{Task: parsed, CreatedAt: cells[4], LineIndex: lineIndex}, nil
}

func parseTaskMetadata(lines []string, tableEnd int) (taskMetadataDocument, int, int, error) {
	markers := make([]int, 0, 1)
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), taskMetadataMarkerPrefix) {
			markers = append(markers, index)
		}
	}
	if len(markers) == 0 {
		return taskMetadataDocument{}, 0, 0, ErrMetadataMissing
	}
	if len(markers) != 1 {
		return taskMetadataDocument{}, 0, 0, ErrMetadataDuplicate
	}
	start := markers[0]
	if strings.TrimSpace(lines[start]) != taskMetadataMarker {
		return taskMetadataDocument{}, 0, 0, fmt.Errorf("%w: unsupported marker version", ErrMetadataInvalid)
	}
	if start <= tableEnd {
		return taskMetadataDocument{}, 0, 0, fmt.Errorf("%w: metadata placement", ErrMetadataInvalid)
	}
	end := -1
	for index := start + 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "-->" {
			end = index
			break
		}
	}
	if end == -1 {
		return taskMetadataDocument{}, 0, 0, fmt.Errorf("%w: unterminated metadata", ErrMetadataInvalid)
	}
	for _, line := range lines[end+1:] {
		if strings.TrimSpace(line) != "" {
			return taskMetadataDocument{}, 0, 0, fmt.Errorf("%w: metadata must be last", ErrMetadataInvalid)
		}
	}
	payload := []byte(strings.Join(lines[start+1:end], "\n"))
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return taskMetadataDocument{}, 0, 0, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var metadata taskMetadataDocument
	if err := decoder.Decode(&metadata); err != nil {
		return taskMetadataDocument{}, 0, 0, fmt.Errorf("%w: JSON payload", ErrMetadataInvalid)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return taskMetadataDocument{}, 0, 0, err
	}
	if metadata.SchemaVersion != taskMetadataSchema || metadata.Tasks == nil {
		return taskMetadataDocument{}, 0, 0, fmt.Errorf("%w: schema", ErrMetadataInvalid)
	}
	return metadata, start, end, nil
}

func rejectDuplicateJSONKeys(payload []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	if err := inspectJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func inspectJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%w: JSON payload", ErrMetadataInvalid)
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("%w: JSON object", ErrMetadataInvalid)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("%w: JSON object key", ErrMetadataInvalid)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("%w: JSON key %s", ErrMetadataDuplicate, key)
			}
			seen[key] = struct{}{}
			if err := inspectJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := inspectJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: JSON delimiter", ErrMetadataInvalid)
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("%w: JSON payload", ErrMetadataInvalid)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON content", ErrMetadataInvalid)
	}
	return nil
}

func combineTaskRows(rows []taskTableRow, metadata taskMetadataDocument) error {
	if len(rows) != len(metadata.Tasks) {
		return fmt.Errorf("%w: Task ID set", ErrMetadataMismatch)
	}
	for index := range rows {
		entry, exists := metadata.Tasks[rows[index].Task.ID]
		if !exists {
			return fmt.Errorf("%w: missing %s", ErrMetadataMismatch, rows[index].Task.ID)
		}
		if entry.Version == 0 || strings.TrimSpace(entry.TableDigest) == "" {
			return fmt.Errorf("%w: Task metadata fields", ErrMetadataInvalid)
		}
		if entry.TableDigest != digestTaskRow(rows[index]) {
			return fmt.Errorf("%w: digest for %s", ErrMetadataMismatch, rows[index].Task.ID)
		}
		rows[index].Task.Version = entry.Version
		rows[index].Task.LastFailureReason = entry.LastFailureReason
		rows[index].Task.HoldReason = entry.HoldReason
		if err := validatePersistedTask(rows[index].Task); err != nil {
			return err
		}
	}
	return nil
}

func validatePersistedTask(current task.Task) error {
	if err := current.Validate(); err != nil {
		return fmt.Errorf("%w: persisted Task", ErrMetadataInvalid)
	}
	if current.Status == task.StatusOnHold {
		if strings.TrimSpace(current.HoldReason) == "" {
			return fmt.Errorf("%w: held Task reason", ErrMetadataMismatch)
		}
	} else if current.HoldReason != "" {
		return fmt.Errorf("%w: unexpected hold reason", ErrMetadataMismatch)
	}
	if current.LastFailureReason != "" && strings.TrimSpace(current.LastFailureReason) == "" {
		return fmt.Errorf("%w: blank failure reason", ErrMetadataInvalid)
	}
	return nil
}

func metadataForRow(row taskTableRow) taskMetadata {
	return taskMetadata{
		Version:           row.Task.Version,
		LastFailureReason: row.Task.LastFailureReason,
		HoldReason:        row.Task.HoldReason,
		TableDigest:       digestTaskRow(row),
	}
}

func digestTaskRow(row taskTableRow) string {
	assigneeID := ""
	if row.Task.AssigneeID != nil {
		assigneeID = *row.Task.AssigneeID
	}
	canonical := strings.Join([]string{
		row.Task.ID,
		row.Task.Title,
		string(row.Task.Status),
		assigneeID,
		row.CreatedAt,
	}, "\x00")
	digest := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (document managedTaskDocument) render() (string, error) {
	if document.metadata.Tasks == nil {
		return "", fmt.Errorf("%w: missing Task map", ErrMetadataInvalid)
	}
	lines := append([]string(nil), document.lines...)
	rowLines := make([]string, len(document.rows))
	for index, row := range document.rows {
		rowLines[index] = renderTaskRow(row)
	}
	tableStart := document.tableEnd - document.originalRows + 1
	if tableStart < 0 || tableStart > len(lines) {
		return "", fmt.Errorf("%w: table layout", ErrInvalidDocument)
	}
	lines = append(lines[:tableStart], append(rowLines, lines[document.tableEnd+1:]...)...)

	metadataLines, err := renderTaskMetadataLines(document.metadata)
	if err != nil {
		return "", err
	}

	offset := len(rowLines) - document.originalRows
	metadataStart := document.metadataStart + offset
	metadataEnd := document.metadataEnd + offset
	lines = append(lines[:metadataStart], append(metadataLines, lines[metadataEnd+1:]...)...)
	result := strings.Join(lines, document.newline)
	if document.trailingNewline {
		result += document.newline
	}
	return result, nil
}

func renderTaskMetadataLines(metadata taskMetadataDocument) ([]string, error) {
	metadataBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: encode metadata", ErrMetadataInvalid)
	}
	lines := append([]string{taskMetadataMarker}, strings.Split(string(metadataBytes), "\n")...)
	return append(lines, "-->"), nil
}

func renderTaskRow(row taskTableRow) string {
	assigneeID := "未割当"
	if row.Task.AssigneeID != nil {
		assigneeID = *row.Task.AssigneeID
	}
	return "| " + strings.Join([]string{
		row.Task.ID,
		row.Task.Title,
		string(row.Task.Status),
		assigneeID,
		row.CreatedAt,
	}, " | ") + " |"
}

func sameCells(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func taskStoreContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidInput)
	}
	return ctx.Err()
}

func jstLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		return time.FixedZone("JST", 9*60*60)
	}
	return location
}

func (osAtomicReplacer) Replace(path string, content []byte, mode fs.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".workspace-os.*.tmp")
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return &AtomicWriteError{Stage: "rename", Err: err}
	}
	committed = true
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

var _ task.Store = (*TaskStore)(nil)
