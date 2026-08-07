package runner

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Registry resolves an employee model value to a named Runner. Configuration
// is concurrency-safe and explicit; no implicit provider fallback exists.
type Registry struct {
	mu           sync.RWMutex
	runners      map[string]Runner
	modelRunners map[string]string
}

func NewRegistry() *Registry {
	return &Registry{
		runners:      make(map[string]Runner),
		modelRunners: make(map[string]string),
	}
}

func (registry *Registry) Register(registered Runner) error {
	if isNil(registered) {
		return ErrInvalidRunner
	}
	name := strings.TrimSpace(registered.Name())
	if name == "" {
		return ErrInvalidRunner
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.runners[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateRunner, name)
	}
	registry.runners[name] = registered
	return nil
}

// MapModel records configuration without requiring registration order. Resolve
// distinguishes an unknown model from a configured-but-unavailable Runner.
func (registry *Registry) MapModel(model, runnerName string) error {
	model = strings.TrimSpace(model)
	runnerName = strings.TrimSpace(runnerName)
	if model == "" {
		return ErrInvalidModel
	}
	if runnerName == "" {
		return ErrInvalidRunner
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.modelRunners[model]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateModelMapping, model)
	}
	registry.modelRunners[model] = runnerName
	return nil
}

func (registry *Registry) Resolve(model string) (Runner, error) {
	model = strings.TrimSpace(model)
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	runnerName, exists := registry.modelRunners[model]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrUnknownModel, model)
	}
	resolved, exists := registry.runners[runnerName]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrRunnerNotRegistered, runnerName)
	}
	return resolved, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
