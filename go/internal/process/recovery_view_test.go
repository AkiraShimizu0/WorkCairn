package process

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/recovery"
)

const recoveryViewTestSecretMarker = "PROVIDER_SECRET_MARKER_MUST_NOT_APPEAR_IN_RECOVERY_VIEW"

func validRecoveryFindingForKind(kind recovery.FindingKind) recovery.Finding {
	finding := recovery.Finding{
		ID: "RECOVERY-001", Kind: kind, Severity: recovery.SeverityWarning, Certainty: recovery.CertaintyConfirmed,
		TaskID: "TASK-001", References: []string{}, Detail: "safe canonical detail",
		Recoverable: false, RecommendedAction: recovery.ActionNone,
	}
	switch kind {
	case recovery.FindingTaskCompletionPending:
		finding.Recoverable, finding.RecommendedAction = true, recovery.ActionCompleteTask
	case recovery.FindingTaskExecutionInterrupted:
		finding.Recoverable, finding.RecommendedAction = true, recovery.ActionFailAndHold
	}
	return finding
}

func validRecoveryReportForTest(findings ...recovery.Finding) recovery.Report {
	return recovery.Report{
		SchemaVersion: recovery.SchemaVersion, ProjectName: "ToDoアプリ",
		Healthy: len(findings) == 0, TaskCount: 1, Findings: findings,
	}
}

func TestProjectRecoveryInspectionViewAcceptsEveryValidFindingKind(t *testing.T) {
	kinds := []recovery.FindingKind{
		recovery.FindingTaskCompletionPending, recovery.FindingTaskExecutionInterrupted,
		recovery.FindingCompletedDeliverableMissing, recovery.FindingDeliverableConflict,
		recovery.FindingArtifactInvalid, recovery.FindingReviewProjectionMissing,
		recovery.FindingReviewCanonicalMissing, recovery.FindingRevisionTaskMissing,
		recovery.FindingAuditUnverifiable, recovery.FindingResidualTemporaryState,
		recovery.FindingCommandIncomplete,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			report := validRecoveryReportForTest(validRecoveryFindingForKind(kind))
			view, err := ProjectRecoveryInspectionView(report)
			if err != nil {
				t.Fatalf("ProjectRecoveryInspectionView() error = %v", err)
			}
			if len(view.Findings) != 1 || view.Findings[0].Kind != kind {
				t.Fatalf("ProjectRecoveryInspectionView() = %#v", view)
			}
		})
	}
}

func TestProjectRecoveryInspectionViewAcceptsEverySeverityAndCertainty(t *testing.T) {
	for _, severity := range []recovery.Severity{recovery.SeverityWarning, recovery.SeverityCritical} {
		finding := validRecoveryFindingForKind(recovery.FindingArtifactInvalid)
		finding.Severity = severity
		if _, err := ProjectRecoveryInspectionView(validRecoveryReportForTest(finding)); err != nil {
			t.Fatalf("severity %q: error = %v", severity, err)
		}
	}
	for _, certainty := range []recovery.Certainty{recovery.CertaintyConfirmed, recovery.CertaintyUnverifiable} {
		finding := validRecoveryFindingForKind(recovery.FindingArtifactInvalid)
		finding.Certainty = certainty
		if _, err := ProjectRecoveryInspectionView(validRecoveryReportForTest(finding)); err != nil {
			t.Fatalf("certainty %q: error = %v", certainty, err)
		}
	}
}

func TestProjectRecoveryInspectionViewAcceptsEveryValidRecoverableActionPair(t *testing.T) {
	cases := []struct {
		recoverable bool
		action      recovery.Action
	}{
		{true, recovery.ActionCompleteTask},
		{true, recovery.ActionFailAndHold},
		{false, recovery.ActionNone},
	}
	for _, current := range cases {
		finding := validRecoveryFindingForKind(recovery.FindingArtifactInvalid)
		finding.Recoverable, finding.RecommendedAction = current.recoverable, current.action
		if _, err := ProjectRecoveryInspectionView(validRecoveryReportForTest(finding)); err != nil {
			t.Fatalf("recoverable=%v action=%q: error = %v", current.recoverable, current.action, err)
		}
	}
}

func TestProjectRecoveryInspectionViewHealthyReportMarshalsEmptyFindingsArray(t *testing.T) {
	view, err := ProjectRecoveryInspectionView(validRecoveryReportForTest())
	if err != nil {
		t.Fatal(err)
	}
	if !view.Healthy || view.Findings == nil {
		t.Fatalf("ProjectRecoveryInspectionView() = %#v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"findings":[]`) {
		t.Fatalf("healthy view JSON = %s, want empty findings array not null", encoded)
	}
}

func TestProjectRecoveryInspectionViewNeverExposesDetailOrReferences(t *testing.T) {
	finding := validRecoveryFindingForKind(recovery.FindingArtifactInvalid)
	finding.Detail = recoveryViewTestSecretMarker
	finding.References = []string{"プロジェクト/ToDoアプリ/Deliverables/" + recoveryViewTestSecretMarker + ".md"}
	view, err := ProjectRecoveryInspectionView(validRecoveryReportForTest(finding))
	if err != nil {
		t.Fatalf("planted Detail/References must not affect projection success: %v", err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), recoveryViewTestSecretMarker) {
		t.Fatalf("view JSON leaked planted marker: %s", encoded)
	}
	if strings.Contains(string(encoded), `"detail"`) || strings.Contains(string(encoded), `"references"`) {
		t.Fatalf("view JSON must not contain detail/references fields at all: %s", encoded)
	}
}

func TestProjectRecoveryInspectionViewRejectsInvalidReports(t *testing.T) {
	validFinding := validRecoveryFindingForKind(recovery.FindingArtifactInvalid)

	cases := map[string]recovery.Report{
		"wrong schema version": {
			SchemaVersion: recovery.SchemaVersion + 1, ProjectName: "ToDoアプリ", TaskCount: 1,
		},
		"blank project name": func() recovery.Report {
			report := validRecoveryReportForTest()
			report.ProjectName = ""
			return report
		}(),
		"noncanonical project name (leading/trailing whitespace)": func() recovery.Report {
			report := validRecoveryReportForTest()
			report.ProjectName = " ToDoアプリ "
			return report
		}(),
		"embedded CR/LF project name": func() recovery.Report {
			report := validRecoveryReportForTest()
			report.ProjectName = "ToDo\r\nアプリ"
			return report
		}(),
		"negative task count": func() recovery.Report {
			report := validRecoveryReportForTest()
			report.TaskCount = -1
			return report
		}(),
		"healthy/findings mismatch": func() recovery.Report {
			report := validRecoveryReportForTest(validFinding)
			report.Healthy = true
			return report
		}(),
		"blank finding id": func() recovery.Report {
			finding := validFinding
			finding.ID = ""
			return validRecoveryReportForTest(finding)
		}(),
		"duplicate finding id": func() recovery.Report {
			duplicate := validFinding
			return validRecoveryReportForTest(validFinding, duplicate)
		}(),
		"noncanonical finding id (whitespace)": func() recovery.Report {
			finding := validFinding
			finding.ID = " RECOVERY-001 "
			return validRecoveryReportForTest(finding)
		}(),
		"embedded CR/LF finding id": func() recovery.Report {
			finding := validFinding
			finding.ID = "RECOVERY-\r\n001"
			return validRecoveryReportForTest(finding)
		}(),
		"noncanonical related id (whitespace)": func() recovery.Report {
			finding := validFinding
			finding.TaskID = " TASK-001 "
			return validRecoveryReportForTest(finding)
		}(),
		"embedded CR/LF related id": func() recovery.Report {
			finding := validFinding
			finding.TaskID = "TASK-\r\n001"
			return validRecoveryReportForTest(finding)
		}(),
		"unknown kind": func() recovery.Report {
			finding := validFinding
			finding.Kind = recovery.FindingKind("something_new")
			return validRecoveryReportForTest(finding)
		}(),
		"unknown severity": func() recovery.Report {
			finding := validFinding
			finding.Severity = recovery.Severity("urgent")
			return validRecoveryReportForTest(finding)
		}(),
		"unknown certainty": func() recovery.Report {
			finding := validFinding
			finding.Certainty = recovery.Certainty("guessed")
			return validRecoveryReportForTest(finding)
		}(),
		"empty action": func() recovery.Report {
			finding := validFinding
			finding.RecommendedAction = recovery.Action("")
			return validRecoveryReportForTest(finding)
		}(),
		"unknown action": func() recovery.Report {
			finding := validFinding
			finding.RecommendedAction = recovery.Action("delete_task")
			return validRecoveryReportForTest(finding)
		}(),
		"recoverable true with action none": func() recovery.Report {
			finding := validFinding
			finding.Recoverable, finding.RecommendedAction = true, recovery.ActionNone
			return validRecoveryReportForTest(finding)
		}(),
		"recoverable false with non-none action": func() recovery.Report {
			finding := validFinding
			finding.Recoverable, finding.RecommendedAction = false, recovery.ActionCompleteTask
			return validRecoveryReportForTest(finding)
		}(),
	}

	for name, report := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ProjectRecoveryInspectionView(report); !errors.Is(err, ErrUnsupportedRecoveryProjection) {
				t.Fatalf("ProjectRecoveryInspectionView(%q) error = %v, want ErrUnsupportedRecoveryProjection", name, err)
			}
		})
	}
}

func TestInspectRecoveryViewProjectsRealVaultReport(t *testing.T) {
	root := writePlanVault(t)
	startRecoveryTask(t, root)
	writeRecoveryDeliverable(t, root)

	view, err := InspectRecoveryView(context.Background(), RecoveryInput{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	if view.ProjectName != "ToDoアプリ" || view.Healthy {
		t.Fatalf("InspectRecoveryView() = %#v", view)
	}
	found := false
	for _, finding := range view.Findings {
		if finding.Kind == recovery.FindingTaskCompletionPending && finding.RelatedID == "TASK-001" {
			found = true
		}
		if finding.Recoverable && finding.RecommendedAction == recovery.ActionNone {
			t.Fatalf("invariant violated in real projection: %#v", finding)
		}
	}
	if !found {
		t.Fatalf("InspectRecoveryView() missing expected finding: %#v", view)
	}
}

// TestInspectRecoveryViewRejectsNoncanonicalProjectNameBeforeAnyVaultRead
// proves the raw, untrimmed input.ProjectName is validated before
// InspectRecovery (and therefore before any Vault reader construction or
// filesystem read) ever runs. VaultRoot is deliberately a nonexistent path:
// if the ordering were wrong and code reached InspectRecovery anyway, the
// resulting error would be a Vault-related error, not
// ErrUnsupportedRecoveryProjection.
func TestInspectRecoveryViewRejectsNoncanonicalProjectNameBeforeAnyVaultRead(t *testing.T) {
	cases := map[string]string{
		"leading whitespace":  " ToDoアプリ",
		"trailing whitespace": "ToDoアプリ ",
		"embedded CR/LF":      "ToDo\r\nアプリ",
		"blank":               "   ",
	}
	for name, projectName := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := InspectRecoveryView(context.Background(), RecoveryInput{
				VaultRoot: "/nonexistent/does-not-exist/" + t.Name(), ProjectName: projectName,
			})
			if !errors.Is(err, ErrUnsupportedRecoveryProjection) {
				t.Fatalf("InspectRecoveryView(%q) error = %v, want ErrUnsupportedRecoveryProjection (proves pre-I/O rejection)", projectName, err)
			}
		})
	}
}
