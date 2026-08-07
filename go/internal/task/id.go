package task

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

var taskIDPattern = regexp.MustCompile(`^TASK-(\d{3,})$`)

var (
	ErrInvalidTaskID   = errors.New("invalid task ID")
	ErrDuplicateTaskID = errors.New("duplicate task ID")
)

func ParseTaskID(taskID string) (int, error) {
	match := taskIDPattern.FindStringSubmatch(taskID)
	if match == nil {
		return 0, fmt.Errorf("%w: %s", ErrInvalidTaskID, taskID)
	}
	number, err := strconv.Atoi(match[1])
	if err != nil || number < 1 || FormatTaskID(number) != taskID {
		return 0, fmt.Errorf("%w: %s", ErrInvalidTaskID, taskID)
	}
	return number, nil
}

func FormatTaskID(number int) string {
	return fmt.Sprintf("TASK-%03d", number)
}

func NextTaskID(existingIDs []string) (string, error) {
	seen := make(map[string]struct{}, len(existingIDs))
	maximum := 0
	for _, taskID := range existingIDs {
		number, err := ParseTaskID(taskID)
		if err != nil {
			return "", err
		}
		if _, exists := seen[taskID]; exists {
			return "", fmt.Errorf("%w: %s", ErrDuplicateTaskID, taskID)
		}
		seen[taskID] = struct{}{}
		if number > maximum {
			maximum = number
		}
	}
	return FormatTaskID(maximum + 1), nil
}
