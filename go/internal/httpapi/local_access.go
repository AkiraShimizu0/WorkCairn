package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
)

const (
	localAccessCookie = "workspace_local_access"
	localIntentHeader = "X-Workspace-Intent"
	localIntentValue  = "mobile-ui.v1"
)

// LocalAccess is a process-local trusted-LAN access boundary. It deliberately
// persists neither the pairing code nor browser sessions.
type LocalAccess struct {
	digest [sha256.Size]byte
}

func NewLocalAccess() (*LocalAccess, string, error) {
	// 128 bits keeps online guessing impractical while remaining copyable via
	// Universal Clipboard during local device pairing.
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	return &LocalAccess{digest: sha256.Sum256([]byte(code))}, code, nil
}

func (access *LocalAccess) valid(value string) bool {
	if access == nil || strings.TrimSpace(value) == "" {
		return false
	}
	digest := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(access.digest[:], digest[:]) == 1
}

func (handler *Handler) EnableLocalAccess(access *LocalAccess) error {
	if access == nil {
		return errors.New("local access is required")
	}
	handler.localAccess = access
	return nil
}

func (handler *Handler) localAccessStatus(response http.ResponseWriter, request *http.Request) {
	authenticated := handler.localAccess == nil || handler.localAccess.authorized(request) || handler.localSetupAvailable(request)
	mode := "loopback"
	if handler.localAccess != nil {
		mode = "trusted_lan"
	}
	writeJSON(response, http.StatusOK, map[string]any{"mode": mode, "authenticated": authenticated, "local_setup_available": handler.localSetupAvailable(request)})
}

func (handler *Handler) localSetupAvailable(request *http.Request) bool {
	if handler.localSetup == nil {
		return false
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return false
	}
	remote := net.ParseIP(host)
	allowed := net.ParseIP(handler.localSetupAddress)
	return remote != nil && (remote.IsLoopback() || allowed != nil && remote.Equal(allowed))
}

func (handler *Handler) pairLocalAccess(response http.ResponseWriter, request *http.Request) {
	if handler.localAccess == nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "LOCAL_ACCESS_DISABLED"})
		return
	}
	if !validLocalIntent(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "LOCAL_ACCESS_ORIGIN_REJECTED"})
		return
	}
	content, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 4096))
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "INVALID_PAIRING_CODE"})
		return
	}
	var payload struct {
		Code string `json:"code"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !handler.localAccess.valid(payload.Code) {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "INVALID_PAIRING_CODE"})
		return
	}
	http.SetCookie(response, &http.Cookie{
		Name: localAccessCookie, Value: payload.Code, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(response, http.StatusOK, map[string]bool{"authenticated": true})
}

func (access *LocalAccess) authorized(request *http.Request) bool {
	cookie, err := request.Cookie(localAccessCookie)
	return err == nil && access.valid(cookie.Value)
}

func validLocalIntent(request *http.Request) bool {
	if request.Header.Get(localIntentHeader) != localIntentValue {
		return false
	}
	origin := strings.TrimSuffix(request.Header.Get("Origin"), "/")
	if origin == "" {
		return false
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return origin == scheme+"://"+request.Host
}

func (handler *Handler) authorizeLocalRequest(response http.ResponseWriter, request *http.Request) bool {
	if handler.localAccess == nil || request.URL.Path == "/v1/local-access/status" || request.URL.Path == "/v1/local-access/pair" ||
		request.URL.Path == "/healthz" {
		return true
	}
	if !strings.HasPrefix(request.URL.Path, "/v1/") {
		return true
	}
	macLocal := handler.localSetupAvailable(request)
	if !macLocal && !handler.localAccess.authorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "LOCAL_ACCESS_REQUIRED"})
		return false
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead && !validLocalIntent(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "LOCAL_ACCESS_ORIGIN_REJECTED"})
		return false
	}
	return true
}
