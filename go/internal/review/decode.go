package review

// DecodeDecision validates one canonical Review JSON document. summary is
// optional here (unlike ParseTypedDecision) so canonical Review JSON
// committed before the Summary field existed still decodes.
func DecodeDecision(content []byte) (Decision, error) {
	return parseDecision(content, false)
}
