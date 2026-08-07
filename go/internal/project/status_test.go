package project

import "testing"

func TestStatusSharedFixture(t *testing.T) {
	fixture := loadProjectFixture(t)
	for _, testCase := range fixture.StatusCases {
		t.Run(testCase.Name, func(t *testing.T) {
			actual := testCase.Status.Valid()
			if actual != testCase.Valid {
				t.Fatalf("Status.Valid() = %v, want %v", actual, testCase.Valid)
			}
			if _, err := ParseStatus(string(testCase.Status)); (err == nil) != testCase.Valid {
				t.Fatalf("ParseStatus() error = %v, valid = %v", err, testCase.Valid)
			}
		})
	}
}

func TestTransitionSharedFixture(t *testing.T) {
	fixture := loadProjectFixture(t)
	for _, testCase := range fixture.TransitionCases {
		t.Run(testCase.Name, func(t *testing.T) {
			if CanTransition(testCase.From, testCase.To) != testCase.Valid {
				t.Fatalf(
					"CanTransition(%s, %s) != %v",
					testCase.From,
					testCase.To,
					testCase.Valid,
				)
			}
			if err := ValidateTransition(testCase.From, testCase.To); (err == nil) != testCase.Valid {
				t.Fatalf("ValidateTransition() error = %v, valid = %v", err, testCase.Valid)
			}
		})
	}
}
