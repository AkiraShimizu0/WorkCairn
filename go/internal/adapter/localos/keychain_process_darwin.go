//go:build darwin

package localos

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const (
	keychainHelperEnvironment = "WORKCAIRN_INTERNAL_KEYCHAIN_HELPER"
	keychainHelperFD          = 3
	keychainOperationRead     = "read"
	keychainOperationUpsert   = "upsert"
	maxKeychainFrameSize      = 64 << 10
)

type credentialKeychain interface {
	Read(context.Context) ([]byte, error)
	Upsert(context.Context, []byte) error
}

type keychainOperationError struct {
	classification CredentialFailure
	status         int32
}

func (operationErr *keychainOperationError) Error() string {
	return "macOS Keychain operation failed"
}

type processCredentialKeychain struct {
	executable  string
	arguments   []string
	environment []string
}

func newProcessCredentialKeychain() credentialKeychain {
	executable, _ := os.Executable()
	return &processCredentialKeychain{executable: executable}
}

func (keychain *processCredentialKeychain) Read(ctx context.Context) ([]byte, error) {
	return keychain.run(ctx, keychainOperationRead, nil)
}

func (keychain *processCredentialKeychain) Upsert(ctx context.Context, credential []byte) error {
	_, err := keychain.run(ctx, keychainOperationUpsert, credential)
	return err
}

type keychainHelperResponse struct {
	status int32
	data   []byte
	err    error
}

func (keychain *processCredentialKeychain) run(ctx context.Context, operation string, credential []byte) ([]byte, error) {
	if keychain.executable == "" {
		return nil, &keychainOperationError{classification: CredentialUnavailable}
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, &keychainOperationError{classification: CredentialCommandFailed}
	}
	parent := os.NewFile(uintptr(fds[0]), "workcairn-keychain-parent")
	child := os.NewFile(uintptr(fds[1]), "workcairn-keychain-child")
	defer parent.Close()

	command := exec.CommandContext(ctx, keychain.executable, keychain.arguments...)
	// Do not inherit the daemon environment: it can contain unrelated Provider
	// credentials. The helper needs only its closed operation marker and any
	// explicit test harness values.
	command.Env = append(filteredEnvironment(keychain.environment, keychainHelperEnvironment), keychainHelperEnvironment+"="+operation)
	command.ExtraFiles = []*os.File{child}
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		child.Close()
		return nil, &keychainOperationError{classification: CredentialCommandFailed}
	}
	child.Close()

	responseChannel := make(chan keychainHelperResponse, 1)
	go func() {
		status, data, responseErr := readKeychainResponse(parent)
		responseChannel <- keychainHelperResponse{status: status, data: data, err: responseErr}
	}()
	if err := writeKeychainFrame(parent, credential); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		response := <-responseChannel
		zeroBytes(response.data)
		return nil, &keychainOperationError{classification: CredentialCommandFailed}
	}
	waitErr := command.Wait()
	response := <-responseChannel
	if ctx.Err() != nil {
		zeroBytes(response.data)
		return nil, ctx.Err()
	}
	if waitErr != nil || response.err != nil {
		zeroBytes(response.data)
		return nil, &keychainOperationError{classification: CredentialCommandFailed}
	}
	if response.status != 0 {
		zeroBytes(response.data)
		return nil, classifyKeychainStatus(response.status)
	}
	return response.data, nil
}

func writeKeychainFrame(writer io.Writer, data []byte) error {
	if uint64(len(data)) > math.MaxUint32 || len(data) > maxKeychainFrameSize {
		return errors.New("keychain payload is too large")
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(data)))
	if err := writeAll(writer, header); err != nil {
		return err
	}
	return writeAll(writer, data)
}

func readKeychainFrame(reader io.Reader) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > maxKeychainFrameSize {
		return nil, errors.New("keychain payload is too large")
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(reader, data); err != nil {
		return nil, err
	}
	return data, nil
}

func writeKeychainResponse(writer io.Writer, status int32, data []byte) error {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(status))
	if err := writeAll(writer, header); err != nil {
		return err
	}
	return writeKeychainFrame(writer, data)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func filteredEnvironment(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func readKeychainResponse(reader io.Reader) (int32, []byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	data, err := readKeychainFrame(reader)
	return int32(binary.BigEndian.Uint32(header)), data, err
}

// RunCredentialHelperIfRequested runs before daemon flag parsing. The helper
// communicates only over inherited anonymous fd 3: credentials never enter
// argv, environment, standard streams, shell history, logs, HTTP, or Ledger.
func RunCredentialHelperIfRequested() (bool, int) {
	operation := os.Getenv(keychainHelperEnvironment)
	if operation == "" {
		return false, 0
	}
	connection := os.NewFile(keychainHelperFD, "workcairn-keychain-helper")
	if connection == nil {
		return true, 2
	}
	defer connection.Close()
	payload, err := readKeychainFrame(connection)
	if err != nil {
		return true, 2
	}
	defer zeroBytes(payload)

	var data []byte
	var status int32
	switch operation {
	case keychainOperationRead:
		if len(payload) != 0 {
			return true, 2
		}
		data, status = nativeKeychainRead(claudeKeychainService, claudeKeychainAccount)
	case keychainOperationUpsert:
		status = nativeKeychainUpsert(claudeKeychainService, claudeKeychainAccount, payload)
	default:
		return true, 2
	}
	defer zeroBytes(data)
	if err := writeKeychainResponse(connection, status, data); err != nil {
		return true, 2
	}
	return true, 0
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func classifyKeychainStatus(status int32) error {
	classification := CredentialCommandFailed
	switch status {
	case errSecItemNotFound:
		classification = CredentialNotFound
	case errSecAuthFailed, errSecReadOnly, errSecInteractionNotAllowed, errSecInteractionRequired,
		errSecDataNotModifiable, errSecNoAccessForItem, errSecMissingEntitlement:
		classification = CredentialPermissionDenied
	case errSecNotAvailable, errSecNoSuchKeychain, errSecDataNotAvailable, errSecDatabaseLocked, errSecUnimplemented:
		classification = CredentialUnavailable
	}
	return &keychainOperationError{classification: classification, status: status}
}
