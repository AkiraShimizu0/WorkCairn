package releaseinspector

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDispatchUnsupportedVersion(t *testing.T) {
	resp := Dispatch(Request{Version: "not-a-real-version", Operation: OperationDetectJSONDuplicateKeys})
	if resp.OK || resp.Error == nil || resp.Error.Code != ErrorCodeUnsupportedVersion {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestDispatchMissingOperation(t *testing.T) {
	resp := Dispatch(Request{Version: ContractVersion})
	if resp.OK || resp.Error == nil || resp.Error.Code != ErrorCodeInvalidRequest {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestDispatchUnknownOperation(t *testing.T) {
	resp := Dispatch(Request{Version: ContractVersion, Operation: "not_a_real_operation"})
	if resp.OK || resp.Error == nil || resp.Error.Code != ErrorCodeUnknownOperation {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestDispatchSelectCertificateInvalidPayloadJSON(t *testing.T) {
	resp := Dispatch(Request{
		Version:   ContractVersion,
		Operation: OperationSelectCertificateBySHA1,
		Payload:   json.RawMessage(`{not valid json`),
	})
	if resp.OK || resp.Error == nil || resp.Error.Code != ErrorCodeInvalidRequest {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestDispatchSelectCertificateUnknownPayloadField(t *testing.T) {
	resp := Dispatch(Request{
		Version:   ContractVersion,
		Operation: OperationSelectCertificateBySHA1,
		Payload:   json.RawMessage(`{"expected_sha1":"aa","pem_bundle":"","unexpected_field":true}`),
	})
	if resp.OK || resp.Error == nil || resp.Error.Code != ErrorCodeInvalidRequest {
		t.Fatalf("resp = %+v, want INVALID_REQUEST for an unknown field", resp)
	}
}

func TestDispatchSelectCertificateEndToEnd(t *testing.T) {
	sha1Hex := syntheticCertSHA1(t, syntheticCertValidPEM)
	payload, err := json.Marshal(selectCertificatePayload{ExpectedSHA1: sha1Hex, PEMBundle: syntheticCertValidPEM})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp := Dispatch(Request{Version: ContractVersion, Operation: OperationSelectCertificateBySHA1, Payload: payload})
	if !resp.OK {
		t.Fatalf("resp = %+v, want ok", resp)
	}
	result, ok := resp.Result.(selectCertificateResult)
	if !ok {
		t.Fatalf("resp.Result type = %T", resp.Result)
	}
	if !result.Valid {
		t.Fatalf("result = %+v, want Valid=true", result)
	}
}

func TestDispatchSelectCertificateZeroMatchesEndToEnd(t *testing.T) {
	payload, err := json.Marshal(selectCertificatePayload{
		ExpectedSHA1: "0000000000000000000000000000000000000000",
		PEMBundle:    syntheticCertValidPEM,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp := Dispatch(Request{Version: ContractVersion, Operation: OperationSelectCertificateBySHA1, Payload: payload})
	if resp.OK || resp.Error == nil || resp.Error.Code != ErrorCodeCertZeroMatches {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestDispatchDetectDuplicateKeysEndToEndDuplicate(t *testing.T) {
	payload, err := json.Marshal(detectDuplicateKeysPayload{JSONDocument: `{"a":1,"a":2}`})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp := Dispatch(Request{Version: ContractVersion, Operation: OperationDetectJSONDuplicateKeys, Payload: payload})
	if !resp.OK {
		t.Fatalf("resp = %+v, want ok (duplicate is a typed result, not a dispatch failure)", resp)
	}
	result, ok := resp.Result.(detectDuplicateKeysResult)
	if !ok {
		t.Fatalf("resp.Result type = %T", resp.Result)
	}
	if !result.HasDuplicate {
		t.Fatalf("result = %+v", result)
	}
}

func TestDispatchDetectDuplicateKeysEndToEndNoDuplicate(t *testing.T) {
	payload, err := json.Marshal(detectDuplicateKeysPayload{JSONDocument: `{"a":1,"b":{"c":2}}`})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp := Dispatch(Request{Version: ContractVersion, Operation: OperationDetectJSONDuplicateKeys, Payload: payload})
	if !resp.OK {
		t.Fatalf("resp = %+v, want ok", resp)
	}
	result, ok := resp.Result.(detectDuplicateKeysResult)
	if !ok {
		t.Fatalf("resp.Result type = %T", resp.Result)
	}
	if result.HasDuplicate {
		t.Fatalf("result = %+v, want HasDuplicate=false", result)
	}
}

func TestDispatchDetectDuplicateKeysResponseNeverContainsInputContent(t *testing.T) {
	cases := []struct {
		name     string
		document string
		marker   string
	}{
		{
			name:     "secret-looking key",
			document: `{"api_secret_key_zzqx":1,"api_secret_key_zzqx":2}`,
			marker:   "api_secret_key_zzqx",
		},
		{
			name:     "absolute-path-looking key",
			document: `{"/Users/zzqx-example/private":1,"/Users/zzqx-example/private":2}`,
			marker:   "zzqx-example",
		},
		{
			name:     "username-looking key",
			document: `{"zzqx_example_username":1,"zzqx_example_username":2}`,
			marker:   "zzqx_example_username",
		},
		{
			name:     "nested duplicate",
			document: `{"outer":{"zzqx_nested_marker":1,"zzqx_nested_marker":2}}`,
			marker:   "zzqx_nested_marker",
		},
		{
			name:     "array inside object duplicate",
			document: `{"items":[{"zzqx_array_marker":1,"zzqx_array_marker":2}]}`,
			marker:   "zzqx_array_marker",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload, err := json.Marshal(detectDuplicateKeysPayload{JSONDocument: testCase.document})
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			resp := Dispatch(Request{Version: ContractVersion, Operation: OperationDetectJSONDuplicateKeys, Payload: payload})
			if !resp.OK {
				t.Fatalf("resp = %+v, want ok", resp)
			}
			result, ok := resp.Result.(detectDuplicateKeysResult)
			if !ok || !result.HasDuplicate {
				t.Fatalf("resp.Result = %+v, want HasDuplicate=true", resp.Result)
			}
			encoded, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			if strings.Contains(string(encoded), testCase.marker) {
				t.Fatalf("response leaked caller-controlled content %q: %s", testCase.marker, encoded)
			}
		})
	}
}

func TestDispatchDetectDuplicateKeysMalformedJSONEndToEnd(t *testing.T) {
	payload, err := json.Marshal(detectDuplicateKeysPayload{JSONDocument: `{"a": }`})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp := Dispatch(Request{Version: ContractVersion, Operation: OperationDetectJSONDuplicateKeys, Payload: payload})
	if resp.OK || resp.Error == nil || resp.Error.Code != ErrorCodeJSONMalformed {
		t.Fatalf("resp = %+v", resp)
	}
}
