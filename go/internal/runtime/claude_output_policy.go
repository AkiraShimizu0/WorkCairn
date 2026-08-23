package runtime

// DefaultClaudeMaxTokens is the single production policy value (ADR-0059)
// for how many output tokens one Claude call may generate. Runtime
// composition is the sole source of truth for this value -- every
// composition root (cmd/workcairn, cmd/workcairn-daemon,
// internal/synthesisacceptance) passes it into ClaudeProcessConfig.MaxTokens
// so Task execution, Review, CEO Plan, Revision, and Synthesis all share
// the identical policy Acceptance itself is measured against.
//
// The Claude Adapter's own defaultMaxTokens (internal/adapter/claude) is a
// defensive fallback only, for a caller that leaves MaxTokens unset (0) --
// it is not a second source of policy. A caller that always passes this
// constant, as every production composition root now does, never exercises
// that fallback in practice.
//
// A real one-shot Synthesis Acceptance benchmark observed the previous
// default (3000) truncate a genuinely multi-priority Synthesis response
// roughly mid-way through (StopReason=max_tokens, ADR-0058 -- the Task
// correctly stopped short of a false "completed" rather than committing
// the truncated text). This value is set to give that class of task
// comfortable headroom to finish, without being raised on guesswork alone;
// see ADR-0059 for the full reasoning, including which parts of the
// decision are grounded in that observed run versus external Provider
// capability knowledge this repository cannot itself verify without a real
// API call.
const DefaultClaudeMaxTokens = 6000
