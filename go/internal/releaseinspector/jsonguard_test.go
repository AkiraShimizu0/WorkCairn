package releaseinspector

import (
	"errors"
	"testing"
)

func TestDetectDuplicateJSONKeysTopLevel(t *testing.T) {
	err := DetectDuplicateJSONKeys([]byte(`{"a":1,"b":2,"a":3}`))
	var dup *DuplicateJSONKeyError
	if !errors.As(err, &dup) {
		t.Fatalf("err = %v, want *DuplicateJSONKeyError", err)
	}
	if dup.Key != "a" || dup.Path != "$" {
		t.Fatalf("dup = %+v, want key=a path=$", dup)
	}
}

func TestDetectDuplicateJSONKeysNestedObject(t *testing.T) {
	err := DetectDuplicateJSONKeys([]byte(`{"outer":{"x":1,"y":2,"x":3}}`))
	var dup *DuplicateJSONKeyError
	if !errors.As(err, &dup) {
		t.Fatalf("err = %v, want *DuplicateJSONKeyError", err)
	}
	if dup.Key != "x" || dup.Path != "$.outer" {
		t.Fatalf("dup = %+v, want key=x path=$.outer", dup)
	}
}

func TestDetectDuplicateJSONKeysObjectInsideArray(t *testing.T) {
	err := DetectDuplicateJSONKeys([]byte(`{"items":[{"id":1},{"id":2,"id":3}]}`))
	var dup *DuplicateJSONKeyError
	if !errors.As(err, &dup) {
		t.Fatalf("err = %v, want *DuplicateJSONKeyError", err)
	}
	if dup.Key != "id" || dup.Path != "$.items[1]" {
		t.Fatalf("dup = %+v, want key=id path=$.items[1]", dup)
	}
}

func TestDetectDuplicateJSONKeysValidWithUnknownNestedFieldsHasNoDuplicate(t *testing.T) {
	document := `{
		"status": "Accepted",
		"submission": {"id": "abc-123", "issues": [{"code": "X"}, {"code": "Y", "detail": {"nested": true}}]},
		"future_field_this_package_does_not_know_about": {"a": 1, "b": {"c": 2, "d": [1,2,3]}}
	}`
	if err := DetectDuplicateJSONKeys([]byte(document)); err != nil {
		t.Fatalf("unexpected error for a valid document with unknown nested fields: %v", err)
	}
}

func TestDetectDuplicateJSONKeysNoDuplicateAcrossSiblingObjects(t *testing.T) {
	// The same key name appearing in two different, non-nested object
	// scopes must never be flagged -- only a repeat within the SAME
	// object scope is a duplicate.
	document := `{"first":{"x":1},"second":{"x":2}}`
	if err := DetectDuplicateJSONKeys([]byte(document)); err != nil {
		t.Fatalf("unexpected error for sibling objects sharing a key name: %v", err)
	}
}

func TestDetectDuplicateJSONKeysMalformedJSONIsNotADuplicateError(t *testing.T) {
	err := DetectDuplicateJSONKeys([]byte(`{"a": }`))
	if err == nil {
		t.Fatalf("expected a parse error for malformed JSON")
	}
	var dup *DuplicateJSONKeyError
	if errors.As(err, &dup) {
		t.Fatalf("malformed JSON must not be reported as a duplicate-key error")
	}
}

func TestDetectDuplicateJSONKeysRejectsTrailingObject(t *testing.T) {
	err := DetectDuplicateJSONKeys([]byte(`{"a":1}{"b":2}`))
	if err == nil {
		t.Fatalf("expected an error for a trailing JSON object after the first value")
	}
}

func TestDetectDuplicateJSONKeysRejectsTrailingArray(t *testing.T) {
	err := DetectDuplicateJSONKeys([]byte(`{"a":1}[1,2]`))
	if err == nil {
		t.Fatalf("expected an error for a trailing JSON array after the first value")
	}
}

func TestDetectDuplicateJSONKeysRejectsTrailingScalar(t *testing.T) {
	err := DetectDuplicateJSONKeys([]byte(`{"a":1} true`))
	if err == nil {
		t.Fatalf("expected an error for a trailing JSON scalar after the first value")
	}
}

func TestDetectDuplicateJSONKeysRejectsTrailingMalformedContent(t *testing.T) {
	err := DetectDuplicateJSONKeys([]byte(`{"a":1} not json`))
	if err == nil {
		t.Fatalf("expected an error for trailing malformed content after the first value")
	}
}

func TestDetectDuplicateJSONKeysAllowsTrailingWhitespaceOnly(t *testing.T) {
	err := DetectDuplicateJSONKeys([]byte("{\"a\":1}  \n\t "))
	if err != nil {
		t.Fatalf("unexpected error for trailing whitespace only: %v", err)
	}
}
