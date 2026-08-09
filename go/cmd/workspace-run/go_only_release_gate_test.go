package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const internalModulePrefix = "github.com/AkiraShimizu0/workspace-os/go/internal/"

func TestGoOnlyReleaseGateCoversProductCapabilities(t *testing.T) {
	capabilities := map[string][]string{
		"release_metadata":      {"version"},
		"normal_operation":      {"migrate-plan", "migrate-apply", "plan", "execute"},
		"ceo_plan":              {"ceo-plan-generate", "ceo-plan-apply-plan", "ceo-plan-apply"},
		"project_task":          {"project-bootstrap-plan", "project-bootstrap-execute", "task-create-plan", "task-create-execute", "project-dependencies-plan", "project-dependencies-create"},
		"organization_identity": {"organization-inspect", "identity-validate", "employee-candidates-validate", "employee-hire-plan", "employee-hire-execute", "employee-rename-plan", "employee-rename-execute", "employee-rename-batch-plan", "employee-id-repair-plan", "employee-id-repair-execute", "organization-sync-plan", "organization-sync-execute"},
		"review":                {"review-plan", "review-execute"},
		"revision":              {"revision-plan", "revision-execute"},
		"reviewed_workflow":     {"workflow-reviewed-plan", "workflow-reviewed-execute"},
		"deliverable_audit":     {"plan", "execute"},
		"recovery":              {"recovery-inspect", "recovery-plan", "recovery-apply"},
		"scheduler":             {"schedule-plan", "schedule-create", "schedule-list"},
		"external_action":       {"action-wordpress-plan", "action-wordpress-publish"},
		"interaction_session":   {"interaction-start-plan", "interaction-start", "interaction-list", "interaction-inspect", "interaction-next", "interaction-plan-generate", "interaction-answer", "interaction-plan-apply", "interaction-workflow-plan", "interaction-workflow-execute", "interaction-action-wordpress-plan", "interaction-action-wordpress-publish"},
	}
	for capability, operations := range capabilities {
		for _, operation := range operations {
			if !knownOperation(operation) {
				t.Fatalf("Go-only capability %s has no product operation %s", capability, operation)
			}
		}
	}
}

func TestGoProductSourcesCannotLaunchExternalProcesses(t *testing.T) {
	goRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	forbiddenImports := map[string]bool{"os/exec": true}
	err = filepath.Walk(goRoot, func(path string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, imported := range file.Imports {
			name, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if forbiddenImports[name] {
				t.Errorf("Go product source can start an external interpreter: %s imports %s", path, name)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "StartProcess" {
				t.Errorf("Go product source can start an external process: %s", path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryHasNoRetiredRuntimeAssets(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	forbiddenNames := map[string]bool{
		".python-version": true,
		".venv":           true,
		"__pycache__":     true,
		"pyproject.toml":  true,
		"uv.lock":         true,
	}
	err = filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(repositoryRoot, path)
		if relErr != nil {
			return relErr
		}
		if entry.IsDir() && (relative == ".git" || relative == "work" || relative == "workspace" || relative == "bin" || relative == "dist") {
			return filepath.SkipDir
		}
		if forbiddenNames[entry.Name()] || (!entry.IsDir() && filepath.Ext(entry.Name()) == ".py") {
			t.Errorf("retired runtime asset remains in repository: %s", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicBetaRepositoryMetadata(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"VERSION", "LICENSE", "README.md", "CHANGELOG.md", "SECURITY.md", "CONTRIBUTING.md"} {
		info, statErr := os.Stat(filepath.Join(repositoryRoot, name))
		if statErr != nil || info.IsDir() {
			t.Errorf("Public Beta repository file is missing: %s", name)
		}
	}
	content, err := os.ReadFile(filepath.Join(repositoryRoot, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(content))
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-beta\.[0-9]+$`).MatchString(version) {
		t.Fatalf("Public Beta VERSION is not a SemVer prerelease: %q", version)
	}
}

func TestV1ArchitectureLayerDependencies(t *testing.T) {
	domainPackages := map[string]bool{
		"action": true, "ceoplan": true, "commandcontract": true, "commandledger": true, "deliverable": true, "event": true, "execution": true, "interaction": true,
		"organization": true, "policy": true, "project": true, "prompt": true,
		"recovery": true, "review": true, "revision": true, "runner": true, "scheduler": true, "task": true,
		"worker": true, "workflow": true,
	}
	forEachProductionGoFile(t, func(path, packageName string, imports []string, _ *ast.File) {
		for _, imported := range imports {
			if !strings.HasPrefix(imported, internalModulePrefix) {
				continue
			}
			dependency := strings.TrimPrefix(imported, internalModulePrefix)
			dependencyLayer := strings.Split(dependency, "/")[0]
			switch {
			case domainPackages[packageName] && isEdgeLayer(dependencyLayer):
				t.Errorf("domain package %s imports edge layer %s: %s", packageName, dependencyLayer, path)
			case packageName == "service" && isEdgeLayer(dependencyLayer):
				t.Errorf("service package imports edge layer %s: %s", dependencyLayer, path)
			case packageName == "kernel" && isEdgeLayer(dependencyLayer):
				t.Errorf("kernel package imports edge layer %s: %s", dependencyLayer, path)
			}
		}
	})
}

func TestTaskLifecycleEventsAreCreatedOnlyByTaskService(t *testing.T) {
	lifecycleTypes := map[string]bool{
		"TaskCreated": true, "TaskStarted": true, "TaskCompleted": true,
		"TaskFailed": true, "TaskHeld": true, "TaskResumed": true,
	}
	forEachProductionGoFile(t, func(path, _ string, _ []string, file *ast.File) {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			function, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || function.Sel.Name != "New" {
				return true
			}
			packageIdentifier, ok := function.X.(*ast.Ident)
			if !ok || packageIdentifier.Name != "event" {
				return true
			}
			eventType, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok || !lifecycleTypes[eventType.Sel.Name] {
				return true
			}
			if filepath.Base(path) != "task_service.go" || filepath.Base(filepath.Dir(path)) != "service" {
				t.Errorf("Task lifecycle Event %s is created outside TaskService: %s", eventType.Sel.Name, path)
			}
			return true
		})
	})
}

func isEdgeLayer(packageName string) bool {
	switch packageName {
	case "adapter", "bootstrap", "process", "runtime":
		return true
	default:
		return false
	}
}

func forEachProductionGoFile(t *testing.T, inspect func(path, packageName string, imports []string, file *ast.File)) {
	t.Helper()
	goRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.Walk(goRoot, func(path string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") || strings.HasSuffix(info.Name(), "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		imports := make([]string, 0, len(file.Imports))
		for _, imported := range file.Imports {
			name, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			imports = append(imports, name)
		}
		inspect(path, file.Name.Name, imports, file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
