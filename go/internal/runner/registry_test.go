package runner

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

type fakeRunner struct{ name string }

func (fake *fakeRunner) Name() string { return fake.name }
func (*fakeRunner) Run(context.Context, worker.RunRequest) (worker.RunResult, error) {
	return worker.RunResult{}, nil
}

func TestRegistryRegisterMapAndResolve(t *testing.T) {
	registry := NewRegistry()
	registered := &fakeRunner{name: "ClaudeRunner"}
	if err := registry.Register(registered); err != nil {
		t.Fatal(err)
	}
	if err := registry.MapModel("Claude Sonnet 5", "ClaudeRunner"); err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.Resolve("Claude Sonnet 5")
	if err != nil || resolved != registered {
		t.Fatalf("Resolve() = %#v, %v", resolved, err)
	}
}

func TestRegistryRejectsInvalidAndDuplicateRegistration(t *testing.T) {
	registry := NewRegistry()
	var nilRunner *fakeRunner
	if err := registry.Register(nilRunner); !errors.Is(err, ErrInvalidRunner) {
		t.Fatalf("nil Register() error = %v", err)
	}
	if err := registry.Register(&fakeRunner{}); !errors.Is(err, ErrInvalidRunner) {
		t.Fatalf("empty Register() error = %v", err)
	}
	if err := registry.Register(&fakeRunner{name: "FakeRunner"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&fakeRunner{name: "FakeRunner"}); !errors.Is(err, ErrDuplicateRunner) {
		t.Fatalf("duplicate Register() error = %v", err)
	}
	if err := registry.MapModel("", "FakeRunner"); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("empty model error = %v", err)
	}
	if err := registry.MapModel("Fake Model", ""); !errors.Is(err, ErrInvalidRunner) {
		t.Fatalf("empty runner name error = %v", err)
	}
	if err := registry.MapModel("Fake Model", "FakeRunner"); err != nil {
		t.Fatal(err)
	}
	if err := registry.MapModel("Fake Model", "OtherRunner"); !errors.Is(err, ErrDuplicateModelMapping) {
		t.Fatalf("duplicate model error = %v", err)
	}
}

func TestRegistryDistinguishesUnknownModelAndMissingRunner(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Resolve("Unknown"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unknown Resolve() error = %v", err)
	}
	if err := registry.MapModel("Known", "MissingRunner"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("Known"); !errors.Is(err, ErrRunnerNotRegistered) {
		t.Fatalf("unregistered Resolve() error = %v", err)
	}
}

func TestRegistrySupportsConcurrentResolve(t *testing.T) {
	registry := NewRegistry()
	_ = registry.Register(&fakeRunner{name: "FakeRunner"})
	_ = registry.MapModel("Fake Model", "FakeRunner")
	var waitGroup sync.WaitGroup
	for range 20 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if _, err := registry.Resolve("Fake Model"); err != nil {
				t.Errorf("Resolve() error = %v", err)
			}
		}()
	}
	waitGroup.Wait()
}
