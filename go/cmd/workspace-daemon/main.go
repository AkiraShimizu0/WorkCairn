package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/bootstrap"
	"github.com/AkiraShimizu0/workspace-os/go/internal/httpapi"
	"github.com/AkiraShimizu0/workspace-os/go/internal/kernel"
	workspaceprocess "github.com/AkiraShimizu0/workspace-os/go/internal/process"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "workspace-daemon stopped:", err)
		os.Exit(1)
	}
}

func run() error {
	var vaultRoot, address string
	var providerTimeout, shutdownTimeout, schedulerInterval time.Duration
	flag.StringVar(&vaultRoot, "vault", "", "Vault root")
	flag.StringVar(&address, "listen", "127.0.0.1:8787", "HTTP listen address")
	flag.DurationVar(&providerTimeout, "provider-timeout", 60*time.Second, "Provider request timeout")
	flag.DurationVar(&shutdownTimeout, "shutdown-timeout", 30*time.Second, "graceful shutdown timeout")
	flag.DurationVar(&schedulerInterval, "scheduler-interval", time.Second, "one-shot Schedule polling interval")
	flag.Parse()
	if vaultRoot == "" || providerTimeout <= 0 || shutdownTimeout <= 0 || schedulerInterval <= 0 {
		return errors.New("Vault root and positive timeouts are required")
	}
	executor, err := httpapi.NewProcessExecutor(vaultRoot, workspaceprocess.ClaudeProcessConfig{
		APIKey: os.Getenv("ANTHROPIC_API_KEY"), ProviderModel: os.Getenv("WORKSPACE_CLAUDE_PROVIDER_MODEL"),
		BaseURL: os.Getenv("WORKSPACE_CLAUDE_BASE_URL"),
	}, &http.Client{Timeout: providerTimeout})
	if err != nil {
		return err
	}
	scheduleStore, err := vault.NewScheduleStore(vaultRoot)
	if err != nil {
		return err
	}
	schedulerService, err := service.NewSchedulerService(scheduleStore, executor, service.SchedulerConfig{
		PollInterval: schedulerInterval, Now: time.Now,
	})
	if err != nil {
		return err
	}
	schedulerKernel, err := kernel.New(bootstrap.DefaultKernelVersion)
	if err != nil {
		return err
	}
	if err := schedulerKernel.RegisterSchedulerService(schedulerService); err != nil {
		return err
	}
	if err := schedulerKernel.Start(); err != nil {
		return err
	}
	schedulerStarted := true
	defer func() {
		if schedulerStarted {
			_ = schedulerKernel.Stop()
		}
	}()
	handler, err := httpapi.NewHandler(executor, executor)
	if err != nil {
		return err
	}
	server, err := httpapi.NewServer(address, handler)
	if err != nil {
		return err
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-signalContext.Done():
	}
	if err := schedulerKernel.Stop(); err != nil {
		return fmt.Errorf("stop scheduler: %w", err)
	}
	schedulerStarted = false
	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	err = <-serverErrors
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
