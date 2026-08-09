package httpapi

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
)

const maxConcurrentAsyncCommands = 4

var (
	errAsyncUnavailable = errors.New("asynchronous command execution is unavailable")
	errAsyncCapacity    = errors.New("asynchronous command capacity is exhausted")
	errAsyncStopping    = errors.New("asynchronous command execution is stopping")
)

type AsyncExecutor interface {
	Executor
	ValidateCommand(Command) error
}

type acceptedCommand struct {
	CommandID string `json:"command_id"`
	StatusURL string `json:"status_url"`
}

type asyncCommandRunner struct {
	executor  AsyncExecutor
	ctx       context.Context
	cancel    context.CancelFunc
	slots     chan struct{}
	wait      sync.WaitGroup
	mu        sync.Mutex
	accepting bool
}

func newAsyncCommandRunner(executor Executor, capacity int) *asyncCommandRunner {
	asyncExecutor, ok := executor.(AsyncExecutor)
	if !ok || capacity <= 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &asyncCommandRunner{
		executor: asyncExecutor, ctx: ctx, cancel: cancel,
		slots: make(chan struct{}, capacity), accepting: true,
	}
}

func (runner *asyncCommandRunner) accept(command Command) error {
	if runner == nil {
		return errAsyncUnavailable
	}
	if err := runner.executor.ValidateCommand(command); err != nil {
		return err
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if !runner.accepting {
		return errAsyncStopping
	}
	select {
	case runner.slots <- struct{}{}:
	default:
		return errAsyncCapacity
	}
	runner.wait.Add(1)
	go func() {
		defer runner.wait.Done()
		defer func() { <-runner.slots }()
		// The durable Command Ledger owns the observable outcome. The HTTP
		// adapter deliberately keeps no second result store.
		_, _ = runner.executor.Execute(runner.ctx, command)
	}()
	return nil
}

func (runner *asyncCommandRunner) shutdown(ctx context.Context) error {
	if runner == nil {
		return nil
	}
	runner.mu.Lock()
	runner.accepting = false
	runner.mu.Unlock()
	done := make(chan struct{})
	go func() {
		runner.wait.Wait()
		close(done)
	}()
	select {
	case <-done:
		runner.cancel()
		return nil
	case <-ctx.Done():
		runner.cancel()
		return ctx.Err()
	}
}

func prefersAsync(value string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(token, ";", 2)[0]), "respond-async") {
			return true
		}
	}
	return false
}

func asyncStatusURL(commandID string) string {
	return "/v1/commands/" + url.PathEscape(commandID) + "?scope=workspace"
}
