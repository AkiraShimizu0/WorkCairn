package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/localos"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/bootstrap"
	"github.com/AkiraShimizu0/workcairn/go/internal/buildinfo"
	"github.com/AkiraShimizu0/workcairn/go/internal/httpapi"
	"github.com/AkiraShimizu0/workcairn/go/internal/kernel"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	workspaceruntime "github.com/AkiraShimizu0/workcairn/go/internal/runtime"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

func main() {
	if handled, exitCode := localos.RunCredentialHelperIfRequested(); handled {
		os.Exit(exitCode)
	}
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
	var vaultRoot, address, credentialSourceValue string
	var providerTimeout, shutdownTimeout, schedulerInterval time.Duration
	var providerFixtureMaxCalls int
	var mobile bool
	flag.StringVar(&vaultRoot, "vault", "", "Vault root")
	flag.StringVar(&address, "listen", "127.0.0.1:8787", "HTTP listen address")
	flag.DurationVar(&providerTimeout, "provider-timeout", workspaceruntime.DefaultProviderRequestTimeout, "Provider request timeout")
	flag.DurationVar(&shutdownTimeout, "shutdown-timeout", 30*time.Second, "graceful shutdown timeout")
	flag.DurationVar(&schedulerInterval, "scheduler-interval", time.Second, "one-shot Schedule polling interval")
	flag.IntVar(&providerFixtureMaxCalls, "provider-fixture-max-calls", 0, "Browser acceptance only: Provider-call Budget for a loopback fixture (0 uses production default)")
	flag.StringVar(&credentialSourceValue, "claude-credential-source", string(workspaceruntime.ClaudeCredentialAutomatic), "Claude credential source: automatic, environment, keychain, or headless-local")
	flag.BoolVar(&mobile, "mobile", false, "serve the paired Web UI on a trusted local network")
	flag.Parse()
	credentialSource, err := workspaceruntime.ParseClaudeCredentialSource(credentialSourceValue)
	if err != nil {
		return err
	}
	managedFirstRun := strings.TrimSpace(vaultRoot) == ""
	providerBaseURL := strings.TrimSpace(os.Getenv("WORKCAIRN_CLAUDE_BASE_URL"))
	if providerTimeout <= 0 || shutdownTimeout <= 0 || schedulerInterval <= 0 || providerFixtureMaxCalls < 0 {
		return errors.New("positive timeouts are required")
	}
	if providerFixtureMaxCalls > 0 && !loopbackProviderFixtureURL(providerBaseURL) {
		return errors.New("provider-fixture-max-calls requires an explicit loopback Provider base URL")
	}
	if vaultRoot == "" {
		var err error
		vaultRoot, err = resolveWorkspaceRoot(context.Background())
		if err != nil {
			return err
		}
	} else {
		var err error
		vaultRoot, err = localos.ValidateWorkspaceRoot(vaultRoot)
		if err != nil {
			return err
		}
	}
	var credentialStore localos.ClaudeCredentialStore
	if credentialSource == workspaceruntime.ClaudeCredentialAutomatic || credentialSource == workspaceruntime.ClaudeCredentialKeychain {
		credentialStore = localos.NewClaudeCredentialStore()
	}
	credentialReaders := workspaceruntime.CredentialReaders{
		Environment: func() string { return os.Getenv("ANTHROPIC_API_KEY") },
	}
	if credentialStore != nil {
		credentialReaders.Keychain = credentialStore.Load
	}
	credentialReaders.HeadlessLocal = func(ctx context.Context) (string, error) {
		configRoot, configErr := os.UserConfigDir()
		if configErr != nil {
			return "", configErr
		}
		store, storeErr := localos.NewHeadlessClaudeCredentialStore(configRoot)
		if storeErr != nil {
			return "", storeErr
		}
		return store.Load(ctx)
	}
	credential, err := resolveDaemonClaudeCredential(context.Background(), credentialSource, credentialReaders)
	if err != nil {
		return err
	}
	executor, err := httpapi.NewProcessExecutorWithActionConfigAndOptions(vaultRoot, workspaceprocess.ClaudeProcessConfig{
		APIKey: credential, BaseURL: providerBaseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
	}, workspaceprocess.WordPressProcessConfig{
		TargetID: os.Getenv("WORKCAIRN_WORDPRESS_TARGET_ID"), BaseURL: os.Getenv("WORKCAIRN_WORDPRESS_BASE_URL"),
		Username: os.Getenv("WORKCAIRN_WORDPRESS_USERNAME"), ApplicationPassword: os.Getenv("WORKCAIRN_WORDPRESS_APPLICATION_PASSWORD"),
	}, workspaceruntime.NewProviderHTTPClient(providerTimeout), httpapi.ProcessExecutorOptions{ProviderFixtureMaxCalls: providerFixtureMaxCalls})
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
	localAddress := addressHost(address)
	if err := handler.EnableLocalSetup(&daemonLocalSetup{
		executor: executor, credentialStore: credentialStore,
		credentialSource: credentialSource, viewer: localos.NewWorkspaceViewer(), vaultRoot: vaultRoot,
	}, localAddress); err != nil {
		return err
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	if managedFirstRun && runtime.GOOS == "darwin" {
		if openErr := localos.NewBrowserOpener().OpenURL(context.Background(), localURL(address)); openErr != nil {
			fmt.Fprintln(os.Stdout, "Open the WorkCairn UI:", localURL(address))
		}
	}
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

func loopbackProviderFixtureURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return false
	}
	if strings.EqualFold(parsed.Hostname(), "localhost") {
		return true
	}
	address := net.ParseIP(parsed.Hostname())
	return address != nil && address.IsLoopback()
}

func resolveDaemonClaudeCredential(ctx context.Context, source workspaceruntime.ClaudeCredentialSource, readers workspaceruntime.CredentialReaders) (string, error) {
	credential, err := workspaceruntime.ResolveClaudeCredential(ctx, source, readers)
	if err != nil && source == workspaceruntime.ClaudeCredentialAutomatic {
		// Backward-compatible interactive first run: automatic has the
		// documented environment -> Keychain precedence and may start with an
		// unconfigured Provider so the Mac setup UI remains reachable. Explicit
		// unattended sources fail closed instead of falling through.
		return "", nil
	}
	return credential, err
}

func resolveWorkspaceRoot(ctx context.Context) (string, error) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate WorkCairn settings: %w", err)
	}
	store, err := localos.NewWorkspaceLocationStore(configRoot)
	if err != nil {
		return "", err
	}
	if root, loadErr := store.Load(); loadErr == nil {
		return root, nil
	} else if !errors.Is(loadErr, localos.ErrNotConfigured) {
		return "", loadErr
	}
	if runtime.GOOS != "darwin" {
		return "", errors.New("no WorkCairn workspace is selected; pass -vault on this platform")
	}
	root, err := localos.NewWorkspaceSelector().Select(ctx)
	if err != nil {
		return "", err
	}
	if err := store.Save(root); err != nil {
		return "", err
	}
	return root, nil
}

type daemonLocalSetup struct {
	executor         *httpapi.ProcessExecutor
	credentialStore  localos.ClaudeCredentialStore
	credentialSource workspaceruntime.ClaudeCredentialSource
	viewer           localos.WorkspaceViewer
	vaultRoot        string
}

func (setup *daemonLocalSetup) ConnectClaude(ctx context.Context) error {
	if setup.credentialSource != workspaceruntime.ClaudeCredentialAutomatic && setup.credentialSource != workspaceruntime.ClaudeCredentialKeychain {
		return &workspaceruntime.CredentialResolutionError{Source: setup.credentialSource, Classification: workspaceruntime.CredentialSourceReadOnly}
	}
	if setup.credentialStore == nil {
		return &workspaceruntime.CredentialResolutionError{Source: setup.credentialSource, Classification: workspaceruntime.CredentialSourceUnavailable}
	}
	credential, err := setup.credentialStore.RequestAndStore(ctx)
	if err != nil {
		return err
	}
	return setup.executor.SetClaudeCredential(credential)
}

func (setup *daemonLocalSetup) RevealWorkspace(ctx context.Context) error {
	return setup.viewer.Reveal(ctx, setup.vaultRoot)
}

func addressHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "127.0.0.1"
	}
	if strings.EqualFold(host, "localhost") {
		return "127.0.0.1"
	}
	return host
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
