package releaseinspector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DuplicateJSONKeyError reports the first duplicate object key found
// while walking a JSON document, including the path of container keys
// and array indices leading to it. It never includes any field value.
type DuplicateJSONKeyError struct {
	Path string
	Key  string
}

func (err *DuplicateJSONKeyError) Error() string {
	return fmt.Sprintf("duplicate JSON key %q at %s", err.Key, err.Path)
}

// DetectDuplicateJSONKeys walks data recursively -- every nested
// object, including objects nested inside arrays -- and returns a
// *DuplicateJSONKeyError for the first duplicate key found within any
// single object scope. A structurally valid document with no duplicate
// key anywhere returns nil. A syntactically invalid document, or a
// document that contains any trailing content (a second JSON value, or
// malformed data) after its first complete value, returns a plain
// (non-duplicate) parse error -- trailing whitespace alone is
// permitted. Field values are tokenized (JSON parsing requires
// scanning past them) but are never interpreted, projected, or
// persisted by this function.
func DetectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if _, err := jsonGuardWalkValue(decoder, "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("document contains trailing content after the first JSON value")
	}
	return nil
}

func jsonGuardWalkValue(decoder *json.Decoder, path string) (json.Token, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			if err := jsonGuardWalkObject(decoder, path); err != nil {
				return nil, err
			}
		case '[':
			if err := jsonGuardWalkArray(decoder, path); err != nil {
				return nil, err
			}
		}
	}
	return token, nil
}

func jsonGuardWalkObject(decoder *json.Decoder, path string) error {
	seen := make(map[string]bool)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("expected object key at %s", path)
		}
		if seen[key] {
			return &DuplicateJSONKeyError{Path: path, Key: key}
		}
		seen[key] = true
		if _, err := jsonGuardWalkValue(decoder, path+"."+key); err != nil {
			return err
		}
	}
	_, err := decoder.Token() // consume closing '}'
	return err
}

func jsonGuardWalkArray(decoder *json.Decoder, path string) error {
	index := 0
	for decoder.More() {
		if _, err := jsonGuardWalkValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
		index++
	}
	_, err := decoder.Token() // consume closing ']'
	return err
}
