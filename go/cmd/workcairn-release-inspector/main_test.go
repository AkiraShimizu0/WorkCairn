package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/releaseinspector"
)

func TestRunValidRequestRoundTrip(t *testing.T) {
	req := releaseinspector.Request{
		Version:   releaseinspector.ContractVersion,
		Operation: releaseinspector.OperationDetectJSONDuplicateKeys,
		Payload:   json.RawMessage(`{"json_document":"{\"a\":1,\"b\":2}"}`),
	}
	input, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var output bytes.Buffer
	code := run(bytes.NewReader(input), &output)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var resp releaseinspector.Response
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.OK || resp.Version != releaseinspector.ContractVersion {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestRunMalformedRequestJSON(t *testing.T) {
	var output bytes.Buffer
	code := run(strings.NewReader("{not valid json"), &output)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var resp releaseinspector.Response
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != releaseinspector.ErrorCodeInvalidRequest {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestRunOversizedRequest(t *testing.T) {
	oversized := bytes.Repeat([]byte("a"), maxRequestBytes+1024)
	var output bytes.Buffer
	code := run(bytes.NewReader(oversized), &output)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var resp releaseinspector.Response
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != releaseinspector.ErrorCodeInvalidRequest {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestRunUnsupportedContractVersion(t *testing.T) {
	req := releaseinspector.Request{Version: "not-a-real-version", Operation: releaseinspector.OperationDetectJSONDuplicateKeys}
	input, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var output bytes.Buffer
	code := run(bytes.NewReader(input), &output)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (a well-formed request/response cycle, even for a semantic failure)", code)
	}
	var resp releaseinspector.Response
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != releaseinspector.ErrorCodeUnsupportedVersion {
		t.Fatalf("resp = %+v", resp)
	}
}
