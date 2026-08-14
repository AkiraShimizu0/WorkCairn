//go:build darwin

package localos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const keychainTestHelperEnvironment = "WORKCAIRN_TEST_KEYCHAIN_HELPER"

type shortWriter struct {
	buffer bytes.Buffer
	limit  int
}

func (writer *shortWriter) Write(value []byte) (int, error) {
	if len(value) > writer.limit {
		value = value[:writer.limit]
	}
	return writer.buffer.Write(value)
}

func TestKeychainFrameHandlesShortWritesWithoutTruncation(t *testing.T) {
	payload := []byte("complete-fake-credential")
	written := &shortWriter{limit: 2}
	if err := writeKeychainFrame(written, payload); err != nil {
		t.Fatal(err)
	}
	got, err := readKeychainFrame(bytes.NewReader(written.buffer.Bytes()))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("round trip = %q, %v", got, err)
	}
}

func TestProcessCredentialKeychainReadAndUpsert(t *testing.T) {
	readKeychain := newTestProcessCredentialKeychain(t, "read-success")
	value, err := readKeychain.Read(context.Background())
	if err != nil || string(value) != "stored-fake-credential" {
		t.Fatalf("Read = %q, %v", value, err)
	}

	upsertKeychain := newTestProcessCredentialKeychain(t, "upsert-success")
	if err := upsertKeychain.Upsert(context.Background(), []byte("complete-fake-credential")); err != nil {
		t.Fatalf("Upsert = %v", err)
	}
}

func TestProcessCredentialKeychainClassifiesNativeStatuses(t *testing.T) {
	tests := []struct {
		mode string
		want CredentialFailure
	}{
		{mode: "permission", want: CredentialPermissionDenied},
		{mode: "interaction-required", want: CredentialPermissionDenied},
		{mode: "unavailable", want: CredentialUnavailable},
		{mode: "locked", want: CredentialUnavailable},
		{mode: "missing", want: CredentialNotFound},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			_, err := newTestProcessCredentialKeychain(t, test.mode).Read(context.Background())
			var operationErr *keychainOperationError
			if !errors.As(err, &operationErr) || operationErr.classification != test.want {
				t.Fatalf("error = %#v, want %s", err, test.want)
			}
		})
	}
}

func TestProcessCredentialKeychainTimeoutKillsAndReapsHelper(t *testing.T) {
	pidPath := t.TempDir() + "/helper.pid"
	keychain := newTestProcessCredentialKeychain(t, "hang")
	keychain.environment = append(keychain.environment, "WORKCAIRN_TEST_KEYCHAIN_PID_PATH="+pidPath)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := keychain.Read(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Read = %v, want deadline exceeded", err)
	}
	payload, readErr := os.ReadFile(pidPath)
	if readErr != nil {
		t.Fatalf("read helper pid: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(payload)))
	if parseErr != nil {
		t.Fatalf("parse helper pid: %v", parseErr)
	}
	if killErr := syscall.Kill(pid, 0); !errors.Is(killErr, syscall.ESRCH) {
		t.Fatalf("helper process %d was not reaped: %v", pid, killErr)
	}
}

func TestProcessCredentialKeychainDoesNotExposeSecretFromFailedHelper(t *testing.T) {
	const secret = "secret-must-never-escape"
	keychain := newTestProcessCredentialKeychain(t, "noisy-failure")
	err := keychain.Upsert(context.Background(), []byte(secret))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed secret: %v", err)
	}
	if strings.Contains(strings.Join(keychain.arguments, " "), secret) || strings.Contains(strings.Join(keychain.environment, " "), secret) {
		t.Fatal("credential entered helper argv or environment")
	}
}

func TestProcessCredentialKeychainDoesNotInheritDaemonEnvironment(t *testing.T) {
	t.Setenv(keychainHelperEnvironment, "recursive-value")
	t.Setenv("ANTHROPIC_API_KEY", "environment-secret-must-not-reach-helper")
	keychain := newTestProcessCredentialKeychain(t, "read-success")
	value, err := keychain.Read(context.Background())
	if err != nil || string(value) != "stored-fake-credential" {
		t.Fatalf("Read = %q, %v", value, err)
	}
}

func TestCredentialHelperUsesFixedServiceAndAccount(t *testing.T) {
	content, err := os.ReadFile("keychain_process_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	for _, call := range []string{
		"nativeKeychainRead(claudeKeychainService, claudeKeychainAccount)",
		"nativeKeychainUpsert(claudeKeychainService, claudeKeychainAccount, payload)",
	} {
		if !strings.Contains(source, call) {
			t.Fatalf("fixed service/account call is missing: %s", call)
		}
	}
}

func newTestProcessCredentialKeychain(t *testing.T, mode string) *processCredentialKeychain {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &processCredentialKeychain{
		executable:  executable,
		arguments:   []string{"-test.run=^TestKeychainProcessHelper$"},
		environment: []string{keychainTestHelperEnvironment + "=" + mode},
	}
}

func TestKeychainProcessHelper(t *testing.T) {
	mode := os.Getenv(keychainTestHelperEnvironment)
	if mode == "" {
		return
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		os.Exit(19)
	}
	connection := os.NewFile(keychainHelperFD, "workcairn-test-keychain-helper")
	if connection == nil {
		os.Exit(20)
	}
	defer connection.Close()
	payload, err := readKeychainFrame(connection)
	if err != nil {
		os.Exit(21)
	}
	defer zeroBytes(payload)
	switch mode {
	case "read-success":
		if len(payload) != 0 {
			os.Exit(22)
		}
		if err := writeKeychainResponse(connection, 0, []byte("stored-fake-credential")); err != nil {
			os.Exit(23)
		}
	case "upsert-success":
		if string(payload) != "complete-fake-credential" {
			os.Exit(24)
		}
		if err := writeKeychainResponse(connection, 0, nil); err != nil {
			os.Exit(25)
		}
	case "permission":
		_ = writeKeychainResponse(connection, errSecAuthFailed, nil)
	case "interaction-required":
		_ = writeKeychainResponse(connection, errSecInteractionRequired, nil)
	case "unavailable":
		_ = writeKeychainResponse(connection, errSecNotAvailable, nil)
	case "locked":
		_ = writeKeychainResponse(connection, errSecDatabaseLocked, nil)
	case "missing":
		_ = writeKeychainResponse(connection, errSecItemNotFound, nil)
	case "hang":
		pidPath := os.Getenv("WORKCAIRN_TEST_KEYCHAIN_PID_PATH")
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(26)
		}
		time.Sleep(10 * time.Second)
	case "noisy-failure":
		_, _ = fmt.Fprintln(os.Stdout, string(payload))
		_, _ = fmt.Fprintln(os.Stderr, string(payload))
		os.Exit(27)
	default:
		_, _ = io.WriteString(os.Stderr, "unknown test helper mode")
		os.Exit(28)
	}
}
