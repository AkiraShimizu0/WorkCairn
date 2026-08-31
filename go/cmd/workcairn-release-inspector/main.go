// Command workcairn-release-inspector is a release-engineering-only
// source tool for macOS Developer ID signing/notarization tooling
// (ADR-0071). It is not part of WorkCairn's public product surface: it
// is not built by `make go-build`, not included in any release
// archive/DMG allow-list, and not part of JSON Contract v1.
//
// It reads exactly one request on stdin and writes exactly one
// response to stdout, both shaped by
// internal/releaseinspector.Request/Response. It never starts an
// external process, touches the filesystem beyond reading stdin and
// writing stdout, or makes a network call.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/releaseinspector"
)

const maxRequestBytes = 1 << 20

func main() {
	os.Exit(run(os.Stdin, os.Stdout))
}

func run(input io.Reader, output io.Writer) (exitCode int) {
	written := false
	defer func() {
		if recover() != nil && !written {
			_ = writeResponse(output, internalErrorResponse())
			exitCode = 1
		}
	}()

	data, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil {
		_ = writeResponse(output, invalidRequestResponse("unable to read request"))
		return 1
	}
	if len(data) > maxRequestBytes {
		_ = writeResponse(output, invalidRequestResponse("request exceeds size limit"))
		return 1
	}

	var req releaseinspector.Request
	if err := decodeStrict(data, &req); err != nil {
		_ = writeResponse(output, invalidRequestResponse("request must be valid JSON"))
		return 1
	}

	if err := writeResponse(output, releaseinspector.Dispatch(req)); err != nil {
		return 1
	}
	written = true
	return 0
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func invalidRequestResponse(message string) releaseinspector.Response {
	return releaseinspector.Response{
		Version: releaseinspector.ContractVersion,
		OK:      false,
		Error:   &releaseinspector.ResponseError{Code: releaseinspector.ErrorCodeInvalidRequest, Message: message},
	}
}

func internalErrorResponse() releaseinspector.Response {
	return releaseinspector.Response{
		Version: releaseinspector.ContractVersion,
		OK:      false,
		Error:   &releaseinspector.ResponseError{Code: releaseinspector.ErrorCodeInternalError, Message: "internal error"},
	}
}

func writeResponse(output io.Writer, resp releaseinspector.Response) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(resp)
}
