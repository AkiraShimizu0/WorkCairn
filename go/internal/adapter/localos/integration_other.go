//go:build !darwin

package localos

import "context"

type unsupportedIntegration struct{}

func NewWorkspaceSelector() WorkspaceSelector         { return unsupportedIntegration{} }
func NewClaudeCredentialStore() ClaudeCredentialStore { return unsupportedIntegration{} }
func NewWorkspaceViewer() WorkspaceViewer             { return unsupportedIntegration{} }
func NewBrowserOpener() BrowserOpener                 { return unsupportedIntegration{} }

func (unsupportedIntegration) Select(context.Context) (string, error) { return "", ErrUnsupported }
func (unsupportedIntegration) Load(context.Context) (string, error)   { return "", ErrNotConfigured }
func (unsupportedIntegration) RequestAndStore(context.Context) (string, error) {
	return "", ErrUnsupported
}
func (unsupportedIntegration) Reveal(context.Context, string) error  { return ErrUnsupported }
func (unsupportedIntegration) OpenURL(context.Context, string) error { return ErrUnsupported }
