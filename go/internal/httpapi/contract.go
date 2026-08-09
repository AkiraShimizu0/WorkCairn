// Package httpapi defines the versioned HTTP edge for Workspace OS commands.
// It depends on application process composition, never the other way around.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/interaction"
)

const (
	ContractVersion            = "workspace-command.v1"
	InteractionContractVersion = "workspace-interaction.v1"
)

var (
	ErrInvalidCommand     = errors.New("invalid HTTP command")
	ErrUnsupportedCommand = errors.New("unsupported HTTP command")
)

type Command struct {
	Version   string          `json:"version"`
	CommandID string          `json:"command_id"`
	Operation string          `json:"operation"`
	Approved  bool            `json:"approved"`
	Payload   json.RawMessage `json:"payload"`
}

func (command Command) Validate() error {
	if command.Version != ContractVersion || commandledger.ValidateCommandID(command.CommandID) != nil ||
		strings.TrimSpace(command.Operation) == "" || command.Operation != strings.TrimSpace(command.Operation) ||
		strings.ContainsAny(command.Operation, "\r\n") || len(command.Payload) == 0 || !json.Valid(command.Payload) {
		return ErrInvalidCommand
	}
	return nil
}

type CommandError struct {
	Code             string `json:"code"`
	Stage            string `json:"stage,omitempty"`
	RecoveryRequired bool   `json:"recovery_required,omitempty"`
}

type Response struct {
	Version   string          `json:"version"`
	CommandID string          `json:"command_id,omitempty"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *CommandError   `json:"error,omitempty"`
}

type InteractionPlanRequest struct {
	Version     string    `json:"version"`
	SessionID   string    `json:"session_id"`
	Request     string    `json:"request"`
	Model       string    `json:"model"`
	CurrentTime time.Time `json:"current_time"`
}

func (request InteractionPlanRequest) Validate() error {
	if request.Version != InteractionContractVersion || interaction.ValidateSessionID(request.SessionID) != nil ||
		strings.TrimSpace(request.Request) == "" || len(request.Request) > 32<<10 ||
		strings.TrimSpace(request.Model) == "" || strings.ContainsAny(request.Model, "\r\n") || request.CurrentTime.IsZero() {
		return ErrInvalidCommand
	}
	return nil
}

func decodePayload(content json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: payload", ErrInvalidCommand)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing payload", ErrInvalidCommand)
	}
	return nil
}
