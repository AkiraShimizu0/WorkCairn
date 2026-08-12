package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/bootstrap"
	"github.com/AkiraShimizu0/workcairn/go/internal/buildinfo"
	"github.com/AkiraShimizu0/workcairn/go/internal/httpapi"
	"github.com/AkiraShimizu0/workcairn/go/internal/kernel"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		if err := json.NewEncoder(os.Stdout).Encode(buildinfo.Current()); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "workcairn-daemon stopped:", err)
		os.Exit(1)
	}
}

func run() error {
	var vaultRoot, address string
	var providerTimeout, shutdownTimeout, schedulerInterval time.Duration
	var mobile bool
	flag.StringVar(&vaultRoot, "vault", "", "Vault root")
	flag.StringVar(&address, "listen", "127.0.0.1:8787", "HTTP listen address")
	flag.DurationVar(&providerTimeout, "provider-timeout", 60*time.Second, "Provider request timeout")
	flag.DurationVar(&shutdownTimeout, "shutdown-timeout", 30*time.Second, "graceful shutdown timeout")
	flag.DurationVar(&schedulerInterval, "scheduler-interval", time.Second, "one-shot Schedule polling interval")
	flag.BoolVar(&mobile, "mobile", false, "serve the paired Web UI on a trusted local network")
	flag.Parse()
	if vaultRoot == "" || providerTimeout <= 0 || shutdownTimeout <= 0 || schedulerInterval <= 0 {
		return errors.New("Vault root and positive timeouts are required")
	}
	executor, err := httpapi.NewProcessExecutorWithActionConfig(vaultRoot, workspaceprocess.ClaudeProcessConfig{
		APIKey: os.Getenv("ANTHROPIC_API_KEY"), BaseURL: os.Getenv("WORKCAIRN_CLAUDE_BASE_URL"),
	}, workspaceprocess.WordPressProcessConfig{
		TargetID: os.Getenv("WORKCAIRN_WORDPRESS_TARGET_ID"), BaseURL: os.Getenv("WORKCAIRN_WORDPRESS_BASE_URL"),
		Username: os.Getenv("WORKCAIRN_WORDPRESS_USERNAME"), ApplicationPassword: os.Getenv("WORKCAIRN_WORDPRESS_APPLICATION_PASSWORD"),
	}, &http.Client{Timeout: providerTimeout})
	if err != nil {
		return err
	}
	providerStatus := executor.InspectProviderStatus()
	if !providerStatus.Configured {
		fmt.Fprintln(os.Stdout, "AI Provider: setup required before Plan generation; the Web UI remains available for read-only use.")
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
	var server *httpapi.Server
	if mobile {
		listenWasSet := false
		flag.Visit(func(current *flag.Flag) {
			if current.Name == "listen" {
				listenWasSet = true
			}
		})
		if !listenWasSet {
			address, err = discoverMobileAddress("8787")
			if err != nil {
				return err
			}
		}
		access, pairingCode, accessErr := httpapi.NewLocalAccess()
		if accessErr != nil {
			return accessErr
		}
		if err := handler.EnableLocalAccess(access); err != nil {
			return err
		}
		server, err = httpapi.NewLocalNetworkServer(address, handler)
		if err == nil {
			fmt.Fprintf(os.Stdout, "WorkCairn mobile UI: %s\nPairing code: %s\nTrusted local network only; do not expose this address to the internet.\n", localURL(address), pairingCode)
		}
	} else {
		server, err = httpapi.NewServer(address, handler)
	}
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

func discoverMobileAddress(port string) (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("discover local network: %w", err)
	}
	candidates := make([]string, 0)
	for _, current := range interfaces {
		if current.Flags&net.FlagUp == 0 || current.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addressErr := current.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr != nil || ip.To4() == nil || !(ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
				continue
			}
			candidates = append(candidates, ip.String())
		}
	}
	if len(candidates) == 0 {
		return "", errors.New("no private local network address found; connect the Mac and iPhone to the same network or pass --listen with a private IP")
	}
	sort.Strings(candidates)
	return net.JoinHostPort(candidates[0], port), nil
}

func localURL(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "http://" + strings.TrimSpace(address) + "/"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}
