package policy

type Outcome string

const (
	OutcomeApproved Outcome = "approved"
	OutcomeRejected Outcome = "rejected"
)

type ApprovalDecision struct {
	Outcome Outcome `json:"outcome"`
	Reason  string  `json:"reason"`
	Policy  string  `json:"policy"`
}

func (decision ApprovalDecision) Approved() bool {
	return decision.Outcome == OutcomeApproved
}

type FailureDecision struct {
	Hold   bool   `json:"hold"`
	Reason string `json:"reason"`
	Policy string `json:"policy"`
}
