package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/action"
	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/ceoplan"
	workspaceprocess "github.com/AkiraShimizu0/workspace-os/go/internal/process"
	"github.com/AkiraShimizu0/workspace-os/go/internal/recovery"
	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

func TestPlanCommandIsReadOnlyAndNeedsNoProviderConfig(t *testing.T) {
	root := writeCommandVault(t)
	before := commandVaultSnapshot(t, root)
	httpConstructed := false
	var output bytes.Buffer
	exitCode := run(context.Background(), append([]string{"plan"}, commandArgs(root)...), &output, commandDependencies{
		lookupEnv: func(string) (string, bool) { t.Fatal("plan read Provider environment"); return "", false },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			httpConstructed = true
			return nil
		},
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exitCode != 0 || !response.OK || response.Version != outputVersion || httpConstructed {
		t.Fatalf("run(plan) exit=%d response=%#v http=%t", exitCode, response, httpConstructed)
	}
	encoded, _ := json.Marshal(response.Result)
	if !strings.Contains(string(encoded), `"executable":true`) || !strings.Contains(string(encoded), `"approval_required":true`) {
		t.Fatalf("plan result = %s", encoded)
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("plan command changed temporary Vault")
	}
}

func TestVersionDoesNotRequireVaultOrRuntimeDependencies(t *testing.T) {
	environmentRead, httpConstructed := false, false
	var output bytes.Buffer
	exitCode := run(context.Background(), []string{"version"}, &output, commandDependencies{
		lookupEnv:     func(string) (string, bool) { environmentRead = true; return "", false },
		newHTTPClient: func(time.Duration) claude.HTTPDoer { httpConstructed = true; return http.DefaultClient },
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exitCode != 0 || !response.OK || environmentRead || httpConstructed {
		t.Fatalf("version response = exit %d %#v env=%t http=%t", exitCode, response, environmentRead, httpConstructed)
	}
}

func TestInteractionStartPlanIsReadOnlyAndGenerationNeedsApprovalBeforeProviderConfig(t *testing.T) {
	root := t.TempDir()
	before := commandVaultSnapshot(t, root)
	var output bytes.Buffer
	planArgs := []string{
		"interaction-start-plan", "--vault", root, "--session-id", "SESSION-CLI-001",
		"--request", "Webアプリを作りたい", "--model", "Claude Sonnet 5", "--at", "2026-08-09T12:00:00Z",
	}
	exitCode := run(context.Background(), planArgs, &output, commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("Interaction plan read Provider environment"); return "", false },
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("Interaction plan constructed HTTP client"); return nil },
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exitCode != 0 || !response.OK || !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("Interaction plan exit=%d response=%#v", exitCode, response)
	}
	encoded, _ := json.Marshal(response.Result)
	var plan workspaceprocess.InteractionStartPlan
	if err := json.Unmarshal(encoded, &plan); err != nil || plan.Session.RequestDigest == "" {
		t.Fatalf("Interaction plan = %#v, %v", plan, err)
	}

	output.Reset()
	startArgs := []string{
		"interaction-start", "--vault", root, "--session-id", "SESSION-CLI-001", "--request", "Webアプリを作りたい",
		"--request-sha256", plan.Session.RequestDigest, "--model", "Claude Sonnet 5", "--at", "2026-08-09T12:00:00Z",
		"--command-id", "CMD-INTERACTION-CLI-START", "--approved",
	}
	if exit := run(context.Background(), startArgs, &output, commandDependencies{}); exit != 0 {
		t.Fatalf("Interaction start exit=%d response=%s", exit, output.String())
	}

	environmentRead, httpConstructed := false, false
	output.Reset()
	exitCode = run(context.Background(), []string{
		"interaction-plan-generate", "--vault", root, "--session-id", "SESSION-CLI-001", "--expected-version", "1",
		"--at", "2026-08-09T12:01:00Z", "--command-id", "CMD-INTERACTION-CLI-PLAN",
	}, &output, commandDependencies{
		lookupEnv:     func(string) (string, bool) { environmentRead = true; return "", false },
		newHTTPClient: func(time.Duration) claude.HTTPDoer { httpConstructed = true; return nil },
	})
	response = decodeCommandResponse(t, output.Bytes())
	if exitCode != 1 || response.Error == nil || response.Error.Code != "APPROVAL_REQUIRED" || environmentRead || httpConstructed {
		t.Fatalf("unapproved generation exit=%d response=%#v env=%t http=%t", exitCode, response, environmentRead, httpConstructed)
	}
}

func TestWorkflowPlanIsReadOnlyAndNeedsNoProviderConfig(t *testing.T) {
	root := writeCommandVault(t)
	before := commandVaultSnapshot(t, root)
	var output bytes.Buffer
	exit := run(context.Background(), []string{"workflow-plan", "--vault", root, "--project-id", "PROJECT-001", "--project", "ToDoアプリ"}, &output, commandDependencies{
		lookupEnv: func(string) (string, bool) { t.Fatal("Workflow plan read Provider environment"); return "", false },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("Workflow plan constructed HTTP client")
			return nil
		},
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exit != 0 || !response.OK || !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("Workflow plan exit=%d response=%#v", exit, response)
	}
}

func TestReviewedWorkflowPlanIsReadOnlyAndNeedsNoProviderConfig(t *testing.T) {
	root := writeCommandVault(t)
	writeCommandFile(t, filepath.Join(root, "社員", "伊藤 健太.md"), "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	before := commandVaultSnapshot(t, root)
	var output bytes.Buffer
	exit := run(context.Background(), []string{
		"workflow-reviewed-plan", "--vault", root, "--project-id", "PROJECT-001", "--project", "ToDoアプリ", "--reviewer", "QA-001",
	}, &output, commandDependencies{
		lookupEnv: func(string) (string, bool) {
			t.Fatal("reviewed Workflow plan read Provider environment")
			return "", false
		},
		now: commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("reviewed Workflow plan constructed HTTP client")
			return nil
		},
	})
	response := decodeCommandResponse(t, output.Bytes())
	encoded, _ := json.Marshal(response.Result)
	if exit != 0 || !response.OK || !bytes.Contains(encoded, []byte(`"review_after_every_task":true`)) ||
		!reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("reviewed Workflow plan exit=%d response=%#v", exit, response)
	}
}

func TestSchedulePlanApprovalCreateAndListUseOnlyTemporaryVault(t *testing.T) {
	root := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "scheduler", "one_shot_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition := string(fixture)
	dependencies := commandDependencies{
		now:       func() time.Time { return commandTestTime() },
		lookupEnv: func(string) (string, bool) { t.Fatal("Schedule read Provider configuration"); return "", false },
	}
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	if code := run(context.Background(), []string{"schedule-plan", "--vault", root, "--schedule-json", definition}, &planOutput, dependencies); code != 0 || !bytes.Contains(planOutput.Bytes(), []byte(`"executable":true`)) {
		t.Fatalf("schedule-plan code=%d output=%s", code, planOutput.String())
	}
	if !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatal("schedule-plan changed temporary Vault")
	}
	var rejected bytes.Buffer
	if code := run(context.Background(), []string{"schedule-create", "--vault", root, "--schedule-json", definition, "--command-id", "CMD-CREATE-SCHEDULE-001"}, &rejected, dependencies); code != 1 || !bytes.Contains(rejected.Bytes(), []byte(`"code":"APPROVAL_REQUIRED"`)) {
		t.Fatalf("unapproved schedule-create code=%d output=%s", code, rejected.String())
	}
	if !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatal("unapproved schedule-create changed temporary Vault")
	}
	var created bytes.Buffer
	if code := run(context.Background(), []string{"schedule-create", "--vault", root, "--schedule-json", definition, "--command-id", "CMD-CREATE-SCHEDULE-001", "--approved"}, &created, dependencies); code != 0 || !bytes.Contains(created.Bytes(), []byte(`"state":"pending"`)) {
		t.Fatalf("schedule-create code=%d output=%s", code, created.String())
	}
	var listed bytes.Buffer
	if code := run(context.Background(), []string{"schedule-list", "--vault", root}, &listed, dependencies); code != 0 || !bytes.Contains(listed.Bytes(), []byte(`"schedule_id":"SCHEDULE-001"`)) {
		t.Fatalf("schedule-list code=%d output=%s", code, listed.String())
	}
}

func TestWordPressActionPlanApprovalPublishAndReplayUseMockProvider(t *testing.T) {
	root := writeCommandVault(t)
	deliverableDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables")
	if err := os.MkdirAll(deliverableDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	deliverableContent := "---\ntype: task-deliverable\nproject: ToDoアプリ\ntask_id: TASK-001\nassignee_id: PLAN-001\nrunner: fake\nexecuted_at: 2026-08-09 10:00:00\n---\n\n# 公開記事\n\n本文\n"
	writeCommandFile(t, filepath.Join(deliverableDirectory, "TASK-001.md"), deliverableContent)
	args := []string{"--vault", root, "--project-id", "PROJECT-001", "--project", "ToDoアプリ", "--task", "TASK-001", "--target", "site-main", "--command-id", "CMD-ACTION-CLI-001"}
	before := commandVaultSnapshot(t, root)
	noProvider := commandDependencies{
		now:       commandTestTime,
		lookupEnv: func(string) (string, bool) { t.Fatal("Action plan or rejection read credentials"); return "", false },
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("Action plan or rejection created HTTP client")
			return nil
		},
	}
	var planOutput bytes.Buffer
	if code := run(context.Background(), append([]string{"action-wordpress-plan"}, args...), &planOutput, noProvider); code != 0 || !bytes.Contains(planOutput.Bytes(), []byte(`"executable":true`)) || !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("Action plan code=%d output=%s", code, planOutput.String())
	}
	publishBaseArgs := append(append([]string(nil), args...), "--source-sha256", action.SourceDigest([]byte(deliverableContent)))
	var rejected bytes.Buffer
	if code := run(context.Background(), append([]string{"action-wordpress-publish"}, publishBaseArgs...), &rejected, noProvider); code != 1 || !bytes.Contains(rejected.Bytes(), []byte(`"code":"APPROVAL_REQUIRED"`)) || !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("Action rejection code=%d output=%s", code, rejected.String())
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":77,"link":"https://example.test/77","status":"publish"}`))
	}))
	defer server.Close()
	environment := map[string]string{
		"WORKSPACE_WORDPRESS_BASE_URL": server.URL, "WORKSPACE_WORDPRESS_USERNAME": "fake-user", "WORKSPACE_WORDPRESS_APPLICATION_PASSWORD": "fake-password",
	}
	dependencies := commandDependencies{
		now: commandTestTime, lookupEnv: func(key string) (string, bool) { value, ok := environment[key]; return value, ok },
		newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}
	publishArgs := append(append([]string{"action-wordpress-publish"}, publishBaseArgs...), "--approved")
	var first bytes.Buffer
	if code := run(context.Background(), publishArgs, &first, dependencies); code != 0 || !bytes.Contains(first.Bytes(), []byte(`"status":"published"`)) || calls != 1 {
		t.Fatalf("Action publish code=%d output=%s calls=%d", code, first.String(), calls)
	}
	var replay bytes.Buffer
	if code := run(context.Background(), publishArgs, &replay, dependencies); code != 0 || replay.String() != first.String() || calls != 1 {
		t.Fatalf("Action replay code=%d output=%s calls=%d", code, replay.String(), calls)
	}
}

func TestOrganizationCommandsNeedNoClockProviderOrEffects(t *testing.T) {
	root := writeCommandVault(t)
	before := commandVaultSnapshot(t, root)
	dependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) {
			t.Fatal("Organization command read Provider environment")
			return "", false
		},
		now: func() time.Time { t.Fatal("Organization command read clock"); return time.Time{} },
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("Organization command constructed HTTP client")
			return nil
		},
	}
	for _, args := range [][]string{
		{"organization-inspect", "--vault", root},
		{"identity-validate", "--vault", root, "--name", "佐藤 蓮"},
	} {
		var output bytes.Buffer
		if exit := run(context.Background(), args, &output, dependencies); exit != 0 {
			t.Fatalf("run(%s) failed: %s", args[0], output.String())
		}
		response := decodeCommandResponse(t, output.Bytes())
		if !response.OK || response.Error != nil {
			t.Fatalf("response = %#v", response)
		}
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("Organization commands changed temporary Vault")
	}
}

func TestRecoveryCommandsInspectPlanAndApplyTemporaryVaultWithoutProvider(t *testing.T) {
	root := writeCommandVault(t)
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(context.Background(), "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	started, err := current.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), started, current.Version); err != nil {
		t.Fatal(err)
	}
	deliverablePath := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
	if err := os.MkdirAll(filepath.Dir(deliverablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, deliverablePath, "---\ntype: task-deliverable\nproject: ToDoアプリ\ntask_id: TASK-001\nassignee_id: PLAN-001\n---\n\n# immutable result\n")

	dependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) { t.Fatal("recovery read Provider environment"); return "", false },
		now:       func() time.Time { t.Fatal("recovery read clock"); return time.Time{} },
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("recovery constructed HTTP client")
			return nil
		},
	}
	base := []string{"--vault", root, "--project", "ToDoアプリ"}
	before := commandVaultSnapshot(t, root)
	var inspectOutput bytes.Buffer
	if exit := run(context.Background(), append([]string{"recovery-inspect"}, base...), &inspectOutput, dependencies); exit != 0 {
		t.Fatalf("recovery-inspect failed: %s", inspectOutput.String())
	}
	inspect := decodeCommandResponse(t, inspectOutput.Bytes())
	encoded, _ := json.Marshal(inspect.Result)
	if !strings.Contains(string(encoded), string(recovery.FindingTaskCompletionPending)) {
		t.Fatalf("recovery inspection = %s", encoded)
	}

	var planOutput bytes.Buffer
	planArgs := append([]string{"recovery-plan"}, base...)
	planArgs = append(planArgs, "--task", "TASK-001", "--action", string(recovery.ActionCompleteTask))
	if exit := run(context.Background(), planArgs, &planOutput, dependencies); exit != 0 {
		t.Fatalf("recovery-plan failed: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("recovery inspect/plan changed the temporary Vault")
	}
	planPath := filepath.Join(t.TempDir(), "recovery-plan.json")
	writeCommandFile(t, planPath, planOutput.String())

	var applyOutput bytes.Buffer
	applyArgs := append([]string{"recovery-apply"}, base...)
	applyArgs = append(applyArgs, "--plan-file", planPath, "--approved")
	if exit := run(context.Background(), applyArgs, &applyOutput, dependencies); exit != 0 {
		t.Fatalf("recovery-apply failed: %s", applyOutput.String())
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusCompleted || stored.Version != started.Version+1 {
		t.Fatalf("recovered Task = %#v, %v", stored, err)
	}
}

func TestProjectBootstrapAndTaskCreationUseGoManagedPathWithoutProvider(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	dependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) { t.Fatal("creation command read Provider environment"); return "", false },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("creation command constructed HTTP client")
			return nil
		},
	}
	projectArgs := []string{"--vault", root, "--project-id", "PROJECT-001", "--project", "ToDoアプリ", "--description", "シンプルなToDoアプリ"}
	for _, operation := range []string{"project-bootstrap-plan", "project-bootstrap-execute"} {
		args := append([]string{operation}, projectArgs...)
		if operation == "project-bootstrap-execute" {
			args = append(args, "--approved")
		}
		var output bytes.Buffer
		if exit := run(context.Background(), args, &output, dependencies); exit != 0 {
			t.Fatalf("%s failed: %s", operation, output.String())
		}
	}
	taskArgs := []string{"--vault", root, "--project", "ToDoアプリ", "--title", "要件を整理する", "--assignee", "PLAN-001"}
	for _, operation := range []string{"task-create-plan", "task-create-execute"} {
		args := append([]string{operation}, taskArgs...)
		if operation == "task-create-execute" {
			args = append(args, "--approved")
		}
		var output bytes.Buffer
		if exit := run(context.Background(), args, &output, dependencies); exit != 0 {
			t.Fatalf("%s failed: %s", operation, output.String())
		}
	}
	dependencyJSON := `{"task_id":"TASK-001","proposal_id":"PROPOSED-001","depends_on":[],"rationale":"最初のTask"}`
	for _, operation := range []string{"project-dependencies-plan", "project-dependencies-create"} {
		args := []string{operation, "--vault", root, "--project", "ToDoアプリ", "--dependency-json", dependencyJSON}
		if operation == "project-dependencies-create" {
			args = append(args, "--approved")
		}
		var output bytes.Buffer
		if exit := run(context.Background(), args, &output, dependencies); exit != 0 {
			t.Fatalf("%s failed: %s", operation, output.String())
		}
	}
	content, err := os.ReadFile(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Tasks.md"))
	if err != nil || !strings.Contains(string(content), "workspace-os-task-metadata:v1") || !strings.Contains(string(content), "TASK-001") {
		t.Fatalf("managed Tasks.md = %s, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Task Dependencies.md")); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeHirePlanAndExecuteNeedNoProvider(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"社員", "会社"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCommandFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeCommandFile(t, filepath.Join(root, "会社", "Workspace State.md"), "---\nupdated_at: 2026-08-01 10:00\n---\n\n## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n\n## 部署\n\n| 部署 | 社員数 | 状態 |\n|---|---:|---|\n")
	dependencies := commandDependencies{lookupEnv: func(string) (string, bool) { t.Fatal("hire read Provider environment"); return "", false }, now: commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("hire created HTTP client"); return nil }}
	base := []string{"--vault", root, "--employee-id", "DEV-001", "--name", "佐藤 蓮", "--department", "開発部", "--role", "Engineer", "--model", "Claude Sonnet 5"}
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	if exit := run(context.Background(), append([]string{"employee-hire-plan"}, base...), &planOutput, dependencies); exit != 0 {
		t.Fatalf("hire plan: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("hire plan changed Vault")
	}
	var executeOutput bytes.Buffer
	args := append(append([]string{"employee-hire-execute"}, base...), "--approved")
	if exit := run(context.Background(), args, &executeOutput, dependencies); exit != 0 {
		t.Fatalf("hire execute: %s", executeOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, "社員", "佐藤 蓮.md")); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeRenamePlanAndExecuteNeedNoProvider(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"社員", "会社"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCommandFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\nname: 田中 美咲\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n\n# 田中 美咲\n\n- 氏名: 田中 美咲\n")
	writeCommandFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| PLAN-001 | 田中 美咲 | Product Manager | 待機中 | なし |\n\n## 部署\n")
	dependencies := commandDependencies{lookupEnv: func(string) (string, bool) { t.Fatal("rename read Provider environment"); return "", false }, now: commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("rename created HTTP client"); return nil }}
	base := []string{"--vault", root, "--employee-id", "PLAN-001", "--old-name", "田中 美咲", "--new-name", "山本 真帆", "--reason", "類似名の解消"}
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	if exit := run(context.Background(), append([]string{"employee-rename-plan"}, base...), &planOutput, dependencies); exit != 0 {
		t.Fatalf("rename plan: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("rename plan changed Vault")
	}
	var executeOutput bytes.Buffer
	args := append(append([]string{"employee-rename-execute"}, base...), "--approved")
	if exit := run(context.Background(), args, &executeOutput, dependencies); exit != 0 {
		t.Fatalf("rename execute: %s", executeOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, "社員", "山本 真帆.md")); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeIDRepairPlanAndExecuteNeedNoProvider(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"社員", "会社"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	employee := func(id string) string {
		return "---\nid: " + id + "\ndepartment: 開発部\nrole: Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n\n- ID: " + id + "\n"
	}
	writeCommandFile(t, filepath.Join(root, "社員", "佐藤 蓮.md"), employee("DEV-002"))
	writeCommandFile(t, filepath.Join(root, "社員", "鈴木 陽菜.md"), employee("DEV-002"))
	writeCommandFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| DEV-002 | 佐藤 蓮 | Engineer | 待機中 | なし |\n| DEV-002 | 鈴木 陽菜 | Engineer | 待機中 | なし |\n\n## 部署\n")
	dependencies := commandDependencies{lookupEnv: func(string) (string, bool) { t.Fatal("ID repair read Provider environment"); return "", false }, now: commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("ID repair created HTTP client"); return nil }}
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	if exit := run(context.Background(), []string{"employee-id-repair-plan", "--vault", root}, &planOutput, dependencies); exit != 0 {
		t.Fatalf("ID repair plan: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("ID repair plan changed Vault")
	}
	repairJSON := `{"name":"鈴木 陽菜","current_id":"DEV-002","proposed_id":"DEV-003"}`
	var executeOutput bytes.Buffer
	args := []string{"employee-id-repair-execute", "--vault", root, "--repair-json", repairJSON, "--approved"}
	if exit := run(context.Background(), args, &executeOutput, dependencies); exit != 0 {
		t.Fatalf("ID repair execute: %s", executeOutput.String())
	}
	content, _ := os.ReadFile(filepath.Join(root, "社員", "鈴木 陽菜.md"))
	if !strings.Contains(string(content), "id: DEV-003") {
		t.Fatalf("Employee ID not repaired: %s", content)
	}
}

func TestCEOPlanGenerateAndApplyUseGoOnlyProductPath(t *testing.T) {
	fixtureContent, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "ceo", "plan_generation_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Request      string          `json:"request"`
		RunnerOutput json.RawMessage `json:"runner_output"`
		ExpectedPlan ceoplan.Plan    `json:"expected_plan"`
		Employees    []struct {
			ID, Name, Department, Role, Model string
		} `json:"employees"`
	}
	if err := json.Unmarshal(fixtureContent, &fixture); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, employee := range fixture.Employees {
		writeCommandFile(t, filepath.Join(root, "社員", employee.Name+".md"), "---\nid: "+employee.ID+"\ndepartment: "+employee.Department+"\nrole: "+employee.Role+"\nmodel: "+employee.Model+"\nstatus: 待機中\n---\n")
	}
	before := commandVaultSnapshot(t, root)
	var unapproved bytes.Buffer
	unapprovedDependencies := commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("unapproved CEO plan read Provider config"); return "", false },
		now:           commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("unapproved CEO plan created HTTP client"); return nil },
	}
	args := []string{"ceo-plan-generate", "--vault", root, "--request", fixture.Request, "--model", "Claude Sonnet 5"}
	if exit := run(context.Background(), args, &unapproved, unapprovedDependencies); exit != 1 {
		t.Fatalf("unapproved exit=%d response=%s", exit, unapproved.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("unapproved CEO generation changed Vault")
	}
	rawOutput, _ := json.Marshal(fixture.RunnerOutput)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(rawOutput)}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
		})
	}))
	defer server.Close()
	environment := map[string]string{"ANTHROPIC_API_KEY": "fake-key", "WORKSPACE_CLAUDE_PROVIDER_MODEL": "claude-test", "WORKSPACE_CLAUDE_BASE_URL": server.URL}
	dependencies := commandDependencies{
		lookupEnv: func(key string) (string, bool) { value, ok := environment[key]; return value, ok },
		now:       commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}
	var generatedOutput bytes.Buffer
	if exit := run(context.Background(), append(args, "--approved"), &generatedOutput, dependencies); exit != 0 {
		t.Fatalf("generate response=%s", generatedOutput.String())
	}
	generated := decodeCommandResponse(t, generatedOutput.Bytes())
	encodedResult, _ := json.Marshal(generated.Result)
	var generationResult service.CEOPlanResult
	if err := json.Unmarshal(encodedResult, &generationResult); err != nil || !reflect.DeepEqual(generationResult.Plan, fixture.ExpectedPlan) {
		t.Fatalf("generation result=%s err=%v", encodedResult, err)
	}
	planJSON, _ := json.Marshal(generationResult.Plan)
	applyArgs := []string{"--vault", root, "--project-id", "PROJECT-001", "--plan-json", string(planJSON)}
	var applyPlanOutput bytes.Buffer
	if exit := run(context.Background(), append([]string{"ceo-plan-apply-plan"}, applyArgs...), &applyPlanOutput, unapprovedDependencies); exit != 0 {
		t.Fatalf("apply plan response=%s", applyPlanOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("CEO apply plan changed Vault")
	}
	var applyOutput bytes.Buffer
	if exit := run(context.Background(), append(append([]string{"ceo-plan-apply"}, applyArgs...), "--approved"), &applyOutput, unapprovedDependencies); exit != 0 {
		t.Fatalf("apply response=%s", applyOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", fixture.ExpectedPlan.ProjectName, "Task Dependencies.md")); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteCommandRequiresFlagBeforeSecretsOrEffects(t *testing.T) {
	root := writeCommandVault(t)
	before := commandVaultSnapshot(t, root)
	var output bytes.Buffer
	exitCode := run(context.Background(), append([]string{"execute"}, commandArgs(root)...), &output, commandDependencies{
		lookupEnv: func(string) (string, bool) { t.Fatal("unapproved command read a secret"); return "", false },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("unapproved command constructed HTTP client")
			return nil
		},
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exitCode != 1 || response.OK || response.Error == nil || response.Error.Code != "APPROVAL_REQUIRED" {
		t.Fatalf("run(execute) exit=%d response=%#v", exitCode, response)
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("unapproved command changed temporary Vault")
	}
}

func TestEveryMutatingCommandRequiresApprovalBeforeIO(t *testing.T) {
	missingVault := filepath.Join(t.TempDir(), "missing-vault")
	missingPlan := filepath.Join(t.TempDir(), "missing-plan.json")
	tests := []struct {
		name, expectedCode string
		args               []string
	}{
		{"migration", "MIGRATION_APPROVAL_REQUIRED", []string{"migrate-apply", "--vault", missingVault, "--project", "P", "--plan-file", missingPlan}},
		{"recovery", "RECOVERY_APPROVAL_REQUIRED", []string{"recovery-apply", "--vault", missingVault, "--project", "P", "--plan-file", missingPlan}},
		{"task execution", "APPROVAL_REQUIRED", []string{"execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P", "--task", "TASK-001", "--command-id", "CMD-UNAPPROVED-001"}},
		{"review", "APPROVAL_REQUIRED", []string{"review-execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P", "--task", "TASK-001", "--reviewer", "REV-001"}},
		{"revision", "APPROVAL_REQUIRED", []string{"revision-execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P", "--task", "TASK-001"}},
		{"workflow", "APPROVAL_REQUIRED", []string{"workflow-execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P", "--command-id", "CMD-WORKFLOW-001"}},
		{"reviewed workflow", "APPROVAL_REQUIRED", []string{"workflow-reviewed-execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P", "--reviewer", "QA-001", "--command-id", "CMD-REVIEWED-WORKFLOW-001"}},
		{"organization sync", "APPROVAL_REQUIRED", []string{"organization-sync-execute", "--vault", missingVault}},
		{"employee hire", "APPROVAL_REQUIRED", []string{"employee-hire-execute", "--vault", missingVault, "--employee-id", "DEV-001", "--name", "佐藤 蓮", "--department", "開発部", "--role", "Engineer", "--model", "Claude Sonnet 5"}},
		{"employee rename", "APPROVAL_REQUIRED", []string{"employee-rename-execute", "--vault", missingVault, "--employee-id", "DEV-001", "--old-name", "佐藤 蓮", "--new-name", "鈴木 陽菜", "--reason", "類似名の解消"}},
		{"employee ID repair", "APPROVAL_REQUIRED", []string{"employee-id-repair-execute", "--vault", missingVault, "--repair-json", `{"name":"佐藤 蓮","current_id":"DEV-001","proposed_id":"DEV-002"}`}},
		{"project bootstrap", "APPROVAL_REQUIRED", []string{"project-bootstrap-execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P"}},
		{"task create", "APPROVAL_REQUIRED", []string{"task-create-execute", "--vault", missingVault, "--project", "P", "--title", "Task"}},
		{"task dependencies", "APPROVAL_REQUIRED", []string{"project-dependencies-create", "--vault", missingVault, "--project", "P", "--dependency-json", `{"task_id":"TASK-001","proposal_id":"PROPOSED-001","depends_on":[],"rationale":"first"}`}},
		{"CEO plan generation", "APPROVAL_REQUIRED", []string{"ceo-plan-generate", "--vault", missingVault, "--request", "Projectを作る", "--model", "Claude Sonnet 5"}},
		{"CEO plan apply", "APPROVAL_REQUIRED", []string{"ceo-plan-apply", "--vault", missingVault, "--project-id", "PROJECT-001", "--plan-json", `{}`}},
	}
	dependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) {
			t.Fatal("unapproved command read Provider environment")
			return "", false
		},
		now: commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("unapproved command constructed HTTP client")
			return nil
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			exit := run(context.Background(), test.args, &output, dependencies)
			response := decodeCommandResponse(t, output.Bytes())
			if exit != 1 || response.OK || response.Error == nil || response.Error.Code != test.expectedCode {
				t.Fatalf("exit=%d response=%#v", exit, response)
			}
			if _, err := os.Stat(missingVault); !os.IsNotExist(err) {
				t.Fatalf("unapproved command touched Vault path: %v", err)
			}
		})
	}
}

func TestExecuteCommandUsesMockProviderAndTemporaryVault(t *testing.T) {
	root := writeCommandVault(t)
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		providerCalls++
		if request.Header.Get("x-api-key") != "fake-api-key" || request.URL.Path != "/v1/messages" {
			t.Error("unexpected mock Provider request")
		}
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{
            "model":"claude-sonnet-5",
            "content":[{"type":"text","text":"# 成果物\n\n本文"}],
            "usage":{"input_tokens":10,"output_tokens":5}
        }`))
	}))
	defer server.Close()
	environment := map[string]string{
		"ANTHROPIC_API_KEY": "fake-api-key", "WORKSPACE_CLAUDE_PROVIDER_MODEL": "claude-sonnet-5",
		"WORKSPACE_CLAUDE_BASE_URL": server.URL,
	}
	args := append([]string{"execute"}, commandArgs(root)...)
	args = append(args, "--approved", "--approval-reference", "human-approval-001", "--execution-id", "EXEC-001", "--command-id", "CMD-CLI-001")
	var output bytes.Buffer
	exitCode := run(context.Background(), args, &output, commandDependencies{
		lookupEnv: func(key string) (string, bool) { value, found := environment[key]; return value, found },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			return server.Client()
		},
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exitCode != 0 || !response.OK || response.Error != nil {
		t.Fatalf("run(execute) exit=%d response=%#v output=%s", exitCode, response, output.String())
	}
	var replayOutput bytes.Buffer
	if replayExit := run(context.Background(), args, &replayOutput, commandDependencies{
		lookupEnv: func(key string) (string, bool) { value, found := environment[key]; return value, found },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			return server.Client()
		},
	}); replayExit != 0 || providerCalls != 1 {
		t.Fatalf("idempotent CLI replay exit=%d providerCalls=%d output=%s", replayExit, providerCalls, replayOutput.String())
	}
	for _, path := range []string{
		filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md"),
		filepath.Join(root, "プロジェクト", "ToDoアプリ", "Audit Log.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing execution output %s: %v", path, err)
		}
	}
}

func TestExecuteCommandNeverWritesSecretToFailureResponse(t *testing.T) {
	root := writeCommandVault(t)
	const secret = "secret-must-not-appear"
	var output bytes.Buffer
	exitCode := run(context.Background(), append(append([]string{"execute"}, commandArgs(root)...), "--approved"), &output, commandDependencies{
		lookupEnv: func(key string) (string, bool) {
			if key == "ANTHROPIC_API_KEY" {
				return secret, true
			}
			return "", false
		},
		now: commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			return http.DefaultClient
		},
	})
	if exitCode != 1 || strings.Contains(output.String(), secret) {
		t.Fatalf("unsafe failure response exit=%d output=%s", exitCode, output.String())
	}
	response := decodeCommandResponse(t, output.Bytes())
	if response.Error == nil || response.Error.Code != "EXECUTION_FAILED" {
		t.Fatalf("response = %#v", response)
	}
}

func TestReviewCommandsPlanWithoutSecretsAndExecuteWithMockProvider(t *testing.T) {
	root := writeCommandVault(t)
	writeCommandFile(t, filepath.Join(root, "社員", "伊藤 健太.md"), "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		content, _ := io.ReadAll(request.Body)
		text := `# 成果物\n\n本文`
		if strings.Contains(string(content), "レビュー方針") {
			text = "## レビュー\n\n問題ありません。\n\nREVIEW_RESULT_JSON_START\n{\"verdict\":\"Approve\",\"issues\":[]}\nREVIEW_RESULT_JSON_END"
		}
		response.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model": "claude-sonnet-5", "content": []map[string]string{{"type": "text", "text": text}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer server.Close()
	environment := map[string]string{
		"ANTHROPIC_API_KEY": "fake-api-key", "WORKSPACE_CLAUDE_PROVIDER_MODEL": "claude-sonnet-5", "WORKSPACE_CLAUDE_BASE_URL": server.URL,
	}
	dependencies := commandDependencies{
		lookupEnv:     func(key string) (string, bool) { value, found := environment[key]; return value, found },
		now:           commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}
	var executeOutput bytes.Buffer
	executeArgs := append(append([]string{"execute"}, commandArgs(root)...), "--approved")
	if exit := run(context.Background(), executeArgs, &executeOutput, dependencies); exit != 0 {
		t.Fatalf("Task execute failed: %s", executeOutput.String())
	}

	reviewArgs := append(commandArgs(root), "--reviewer", "QA-001")
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	planDependencies := commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("review-plan read Provider environment"); return "", false },
		now:           commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("review-plan constructed HTTP client"); return nil },
	}
	if exit := run(context.Background(), append([]string{"review-plan"}, reviewArgs...), &planOutput, planDependencies); exit != 0 {
		t.Fatalf("review-plan failed: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("review-plan changed temporary Vault")
	}

	var reviewOutput bytes.Buffer
	if exit := run(context.Background(), append(append([]string{"review-execute"}, reviewArgs...), "--approved"), &reviewOutput, dependencies); exit != 0 {
		t.Fatalf("review-execute failed: %s", reviewOutput.String())
	}
	response := decodeCommandResponse(t, reviewOutput.Bytes())
	if !response.OK || response.Error != nil {
		t.Fatalf("review response = %#v", response)
	}
	for _, name := range []string{"TASK-001.review.json", "TASK-001.review.md"} {
		if _, err := os.Stat(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Reviews", name)); err != nil {
			t.Fatalf("Review artifact missing: %v", err)
		}
	}
}

func TestReviewExecuteRequiresApprovalBeforeSecrets(t *testing.T) {
	root := writeCommandVault(t)
	args := append(append([]string{"review-execute"}, commandArgs(root)...), "--reviewer", "QA-001")
	var output bytes.Buffer
	exit := run(context.Background(), args, &output, commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("unapproved Review read a secret"); return "", false },
		now:           commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("unapproved Review constructed HTTP client"); return nil },
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exit != 1 || response.Error == nil || response.Error.Code != "APPROVAL_REQUIRED" {
		t.Fatalf("review-execute exit=%d response=%#v", exit, response)
	}
}

func TestRevisionCommandsNeedNeitherProviderSecretsNorHTTP(t *testing.T) {
	root := writeCommandVault(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{"model":"claude-sonnet-5","content":[{"type":"text","text":"# 成果物\n\n本文"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer server.Close()
	var executeOutput bytes.Buffer
	if exit := run(context.Background(), append(append([]string{"execute"}, commandArgs(root)...), "--approved"), &executeOutput, commandDependencies{
		lookupEnv: func(key string) (string, bool) {
			values := map[string]string{"ANTHROPIC_API_KEY": "fake", "WORKSPACE_CLAUDE_PROVIDER_MODEL": "claude-sonnet-5", "WORKSPACE_CLAUDE_BASE_URL": server.URL}
			value, found := values[key]
			return value, found
		},
		now: commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}); exit != 0 {
		t.Fatalf("Task execute failed: %s", executeOutput.String())
	}
	reviews := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Reviews")
	if err := os.MkdirAll(reviews, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, filepath.Join(reviews, "TASK-001.review.json"), `{"verdict":"Request Changes","issues":[{"category":"requirements","severity":"medium","description":"要件不足","suggested_action":"追記する"}]}`+"\n")
	writeCommandFile(t, filepath.Join(reviews, "TASK-001.review.md"), "---\ntype: review\nproject: ToDoアプリ\ntask_id: TASK-001\n---\n")
	dependencies := commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("Revision read Provider environment"); return "", false },
		now:           commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("Revision constructed HTTP client"); return nil },
	}
	args := commandArgs(root)
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	if exit := run(context.Background(), append([]string{"revision-plan"}, args...), &planOutput, dependencies); exit != 0 {
		t.Fatalf("revision-plan failed: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("revision-plan changed temporary Vault")
	}
	var revisionOutput bytes.Buffer
	if exit := run(context.Background(), append(append([]string{"revision-execute"}, args...), "--approved"), &revisionOutput, dependencies); exit != 0 {
		t.Fatalf("revision-execute failed: %s", revisionOutput.String())
	}
	response := decodeCommandResponse(t, revisionOutput.Bytes())
	if !response.OK || response.Error != nil || !strings.Contains(revisionOutput.String(), `"revision_task_id":"TASK-002"`) {
		t.Fatalf("Revision response = %s", revisionOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Revisions", "TASK-002.revision.md")); err != nil {
		t.Fatalf("Revision intent missing: %v", err)
	}
}

func TestReviewPublicationFailureResponsePreservesCommitState(t *testing.T) {
	result := workspaceprocess.ReviewExecutionResult{
		Status: "partial_failure",
		Artifact: &review.Record{
			TaskID: "TASK-001", CanonicalCommitted: true, ProjectionCommitted: true,
		},
		EventID: "event-001",
	}
	response := reviewFailureResponse(&service.ReviewOrchestrationError{
		PublicationErr: errors.New("audit unavailable"),
	}, result)
	if response.Error == nil || response.Error.Code != "REVIEW_EVENT_PUBLISH_FAILED" ||
		!response.Error.CanonicalCommitted || !response.Error.ProjectionCommitted {
		t.Fatalf("response = %#v", response)
	}
}

func TestMigrationCommandsRequireSavedPlanAndExplicitApproval(t *testing.T) {
	root := writeCommandVault(t)
	tasksPath := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Tasks.md")
	managed, err := os.ReadFile(tasksPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Split(string(managed), "<!-- workspace-os-task-metadata:v1")[0]
	if err := os.WriteFile(tasksPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	before := commandVaultSnapshot(t, root)

	var planOutput bytes.Buffer
	exitCode := run(context.Background(), []string{
		"migrate-plan", "--vault", root, "--project", "ToDoアプリ",
	}, &planOutput, commandDependencies{})
	planResponse := decodeCommandResponse(t, planOutput.Bytes())
	if exitCode != 0 || !planResponse.OK || !strings.Contains(planOutput.String(), `"source_revision":"sha256:`) {
		t.Fatalf("migrate-plan exit=%d response=%s", exitCode, planOutput.String())
	}
	if afterPlan := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, afterPlan) {
		t.Fatal("migrate-plan changed temporary Vault")
	}
	planPath := filepath.Join(t.TempDir(), "migration-plan.json")
	if err := os.WriteFile(planPath, planOutput.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var unapprovedOutput bytes.Buffer
	exitCode = run(context.Background(), []string{
		"migrate-apply", "--vault", root, "--project", "ToDoアプリ", "--plan-file", planPath,
	}, &unapprovedOutput, commandDependencies{})
	unapproved := decodeCommandResponse(t, unapprovedOutput.Bytes())
	if exitCode != 1 || unapproved.Error == nil || unapproved.Error.Code != "MIGRATION_APPROVAL_REQUIRED" {
		t.Fatalf("unapproved migrate-apply exit=%d response=%#v", exitCode, unapproved)
	}
	if afterUnapproved := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, afterUnapproved) {
		t.Fatal("unapproved migrate-apply changed temporary Vault")
	}

	var applyOutput bytes.Buffer
	exitCode = run(context.Background(), []string{
		"migrate-apply", "--vault", root, "--project", "ToDoアプリ", "--plan-file", planPath, "--approved",
	}, &applyOutput, commandDependencies{})
	applied := decodeCommandResponse(t, applyOutput.Bytes())
	if exitCode != 0 || !applied.OK {
		t.Fatalf("migrate-apply exit=%d response=%s", exitCode, applyOutput.String())
	}
	updated, err := os.ReadFile(tasksPath)
	if err != nil || !strings.Contains(string(updated), "<!-- workspace-os-task-metadata:v1") {
		t.Fatalf("migrated Tasks.md error=%v content=%s", err, updated)
	}
}

func commandArgs(root string) []string {
	return []string{"--vault", root, "--project-id", "PROJECT-001", "--project", "ToDoアプリ", "--task", "TASK-001"}
}

func commandTestTime() time.Time {
	return time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60))
}

func decodeCommandResponse(t *testing.T, content []byte) commandResponse {
	t.Helper()
	var response commandResponse
	if err := json.Unmarshal(content, &response); err != nil {
		t.Fatalf("invalid command response: %v: %s", err, content)
	}
	return response
}

func writeCommandVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	employeeDirectory := filepath.Join(root, "社員")
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if err := os.MkdirAll(employeeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, filepath.Join(employeeDirectory, "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeCommandFile(t, filepath.Join(projectDirectory, "Project.md"), "---\ntype: project\nname: ToDoアプリ\n---\n\n# ToDoアプリ\n\n## 概要\n\nシンプルなToDo Webアプリを開発する\n")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "vault", "tasks_managed_v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, filepath.Join(projectDirectory, "Tasks.md"), string(fixture))
	writeCommandFile(t, filepath.Join(projectDirectory, "Task Dependencies.md"), "---\ntype: task-dependencies\nproject: ToDoアプリ\n---\n\n| Task ID | Proposed ID | Depends On | Rationale |\n|---|---|---|---|\n| TASK-001 | PROPOSED-001 | なし | 初期Task |\n")
	return root
}

func writeCommandFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commandVaultSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[relative+"/"] = "directory"
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[relative] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
