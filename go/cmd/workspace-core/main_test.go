package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type contractFixture struct {
	Cases []contractCase `json:"cases"`
}

type contractCase struct {
	Name     string          `json:"name"`
	Request  json.RawMessage `json:"request"`
	RawInput *string         `json:"raw_input"`
	Expected json.RawMessage `json:"expected"`
}

func TestContractFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "fixtures", "go_core", "contract_cases.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture contractFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			input := []byte(testCase.Request)
			if testCase.RawInput != nil {
				input = []byte(*testCase.RawInput)
			}
			var output bytes.Buffer
			run(bytes.NewReader(input), &output)
			assertJSONEqual(t, testCase.Expected, output.Bytes())
		})
	}
}

func TestOversizedInput(t *testing.T) {
	var output bytes.Buffer
	run(bytes.NewReader(bytes.Repeat([]byte("x"), maxRequestBytes+1)), &output)
	var actual response
	if err := json.Unmarshal(output.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.OK || actual.Error == nil || actual.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("unexpected response: %#v", actual)
	}
}

func assertJSONEqual(t *testing.T, expectedJSON, actualJSON []byte) {
	t.Helper()
	var expected any
	var actual any
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actualJSON, &actual); err != nil {
		t.Fatalf("invalid CLI response: %v: %s", err, actualJSON)
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("expected %s, got %s", expectedJSON, actualJSON)
	}
}
