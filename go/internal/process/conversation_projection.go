package process

import "context"

// ConversationEntryProjection is one ConversationEntry with the Go-owned
// mention_allowed flag materialized for HTTP/UI consumers (ADR-0047 Option B).
type ConversationEntryProjection struct {
	ConversationEntry
	MentionAllowed bool `json:"mention_allowed"`
}

// ConversationInspection is the read-only HTTP projection for one Interaction
// Session conversation. Entries preserve typed canonical facts in deterministic
// order; mention_allowed is always computed from MentionAllowed().
type ConversationInspection struct {
	Version   string                        `json:"version"`
	SessionID string                        `json:"session_id"`
	Entries   []ConversationEntryProjection `json:"entries"`
}

// InspectConversationInspection builds the additive HTTP read model for one
// Interaction Session. It is read-only and delegates all semantic projection
// to InspectConversation.
func InspectConversationInspection(ctx context.Context, vaultRoot, sessionID string) (ConversationInspection, error) {
	entries, err := InspectConversation(ctx, vaultRoot, sessionID)
	if err != nil {
		return ConversationInspection{}, err
	}
	projected := make([]ConversationEntryProjection, len(entries))
	for index, entry := range entries {
		projected[index] = ConversationEntryProjection{
			ConversationEntry: entry,
			MentionAllowed:    entry.MentionAllowed(),
		}
	}
	return ConversationInspection{
		Version:   ConversationVersion,
		SessionID: sessionID,
		Entries:   projected,
	}, nil
}
