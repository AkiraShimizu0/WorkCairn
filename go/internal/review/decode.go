package review

// DecodeDecision validates one canonical Review JSON document.
func DecodeDecision(content []byte) (Decision, error) {
	return parseDecision(content)
}
