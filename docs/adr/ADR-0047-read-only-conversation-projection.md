# ADR-0047: Read-only Conversation Projection — Typed Facts, No Fabricated Speaker

## Status

Accepted

## Context

WorkCairnのUIは、社長がCEO Intent Contract（ADR-0046）を介して依頼を送信し、会社側がReviewed Workflow（ADR-0024）を通じて自律的に進める「Slackのような会話」体験へ向かっています。しかし現在、UIには次の区別がありません。

- 社長自身の発言
- AI社員同士の実際のやり取り
- 会社で起きた事実（誰が行ったか分かるが、誰に向けたものかは分からないもの）
- WorkCairn runtime自身の通知

さらに、Review結果（reviewer、verdict、summary、issues、suggested action）のような既にcanonical evidenceとして存在する詳細情報も、UIには「伊藤 健太のレビュー」程度の粗い状態でしか届いていません。

Article 7（Events Represent Facts, Not Log Files）は、Domain EventはAudit形式や永続化先を知らない事実であると定めています。Domain Event自体をchat messageへ変質させることはこの原則に反します。一方で、Interaction Session、Task、Review、Revision、Deliverable、Command Ledgerには、会話的に提示できるだけの粒度の高いcanonical evidenceが既に存在します。必要なのは、これらの既存evidenceをread-onlyに集約する新しいpresentation/read modelであり、Domain Event自体の変更ではありません。

もう一つの重大なリスクは、「チャットらしく見せるために事実を作る」ことです。例えばTask assignmentは、canonical evidence上「TaskServiceがTASK-001を佐藤 葵へ割り当てた」という一方向の事実に過ぎず、「誰が佐藤さんへ依頼したか」という人間同士のcommunication factではありません。この区別を曖昧にすると、canonical evidenceにない会話を捏造することになります。

## Decision

`go/internal/process/conversation.go`に、新しいread-only projection関数`InspectConversation(ctx, vaultRoot, sessionID) ([]ConversationEntry, error)`を追加します。既存の`InspectWorkReport`/`InspectCompanyActivity`と同じ思想——既存canonical evidenceを読むだけで、新しい書き込みを一切行わない——に従います。

### Domain Eventとの違い

Domain Event（`go/internal/event`の閉じた型集合）は変更しません。Conversation Projectionは、EventそのものではなくInteraction Turn、Task/Deliverable/Review evidence、Command Ledgerという**既存canonical evidenceを読み出して再集約するpresentation層**です。Event Domainは引き続きAudit形式や永続化先を知らないまま維持されます。

### 4 Category

```go
type ConversationCategory string
const (
    CategoryCEOMessage            ConversationCategory = "ceo_message"
    CategoryDirectedCommunication ConversationCategory = "directed_communication"
    CategoryCompanyFact           ConversationCategory = "company_fact"
    CategorySystem                ConversationCategory = "system"
)
```

- **CEO Message**: CEOの元request、または確認済みclarification回答。`Speaker`/`Recipient`/`Subject`は常にnilです——Categoryそのものが「これは社長の発言である」という信号です。
- **Directed Communication**: canonical evidenceで送信者と受信者の**両方**が確定する場合だけ。例: Reviewer → Maker のRequest Changes（`WorkflowEvidence.ReviewerID`と`Task.AssigneeID`が両方確定）、Maker → Reviewer のRevision completed（同一Workflow内の同一Reviewerが確定している場合のみ）。
- **Company Fact**: actorまたはsubjectの片方だけが確定し、方向性を証明できない事実。Task assignment（TaskServiceの一方的な行為であり、人間の発言ではない）、Deliverable ready、Review Approved（後述）、Task/Request completionなど。
- **System**: FailureEnvelopeのような、人間actorを持たないWorkCairn runtime事実。

### Speaker / Recipient / Subjectの意味

```go
type ActorRef struct {
    EmployeeID string `json:"employee_id"`
    Name       string `json:"name,omitempty"`
}
```

`Speaker`と`Recipient`はCategoryDirectedCommunicationでのみ設定し、canonical evidenceが両方を確定できる場合だけ埋めます。`Subject`はCategoryCompanyFactで、単一のactorが分かる場合だけ設定し、方向性は一切意味しません。Employee IDは永続参照（Article 10）、Nameは表示専用です。

### Directed Communication成立条件

送信者・受信者の**両方**がcanonical evidenceから確定した場合だけです。片方しか分からない場合はCompanyFactへ格下げします。

- Reviewer → Maker のRequest Changes: `WorkflowTaskEvidence.Verdict == RequestChanges`かつReviewer・Maker双方のEmployee IDが現在のOrganization inventoryで解決できる場合。
- **Reviewer + Makerが両方確定していても、Approve verdictはDirected Communicationにしません。** Request Changesは特定の相手への具体的な行動要求という性質上「発言」として扱えますが、Approveは単なる完了事実であり、誰かへ「話しかけた」わけではないためCompany Factのままとします。

#### Maker → Reviewer のRevision completedがDirected Communicationとして安全な根拠

推測ではなく、次の2点をコード上で直接確認した上での判断です。

1. **`WorkflowEvidence.ReviewerID`は1つのCommand実行につき単一値であり、契約（JSON shape）上もper-taskのReviewer IDフィールドは存在しません。** `go/internal/process/reviewed_workflow.go`の`ExecuteReviewedWorkflow`は、`reviewer := reviewedWorkflowReviewerFunc(func(...) { return ExecuteReview(..., ReviewerID: input.ReviewerID, ...) })`という一つのclosureを一度だけ構築し、これを`service.NewReviewedWorkflowRunService`へ渡します。このclosureは`input.ReviewerID`（`ExecuteReviewedWorkflowInput`のトップレベル単一値）をキャプチャしており、そのCommand実行が処理する**すべてのTask・すべてのRevision再Review**が同じclosure＝同じReviewerID経由で呼ばれます。per-task・per-revisionで別のReviewerを解決する分岐はコード上存在しません。これは実装のたまたまの一致ではなく、closureが構造的に強制する契約です。
2. **`InspectConversation`はReviewerをTurnごとに再取得し、Turnをまたいで持ち越しません。** `reviewer := employeesByID[strings.TrimSpace(turn.Workflow.ReviewerID)]`は`for turnIndex, turn := range record.Turns`ループの内側、`if turn.Workflow != nil`ブロック内で毎Turnごとに再宣言されます。同一Sessionが複数の`interaction.workflow.execute`呼び出し（例えば`max_tasks`超過による継続）にまたがり、それぞれ異なるReviewerIDで実行された場合でも、後のTurnのRevision entryが前のTurnのReviewerを引き継ぐことはありません。`workflowTaskConversationEntries`が`Reviewer`を明示的な引数として受け取る設計自体が、この取り違えを構造的に防いでいます。

この2点を`TestWorkflowTaskConversationEntriesReviewerNeverCarriesAcrossTurns`で固定しました——2つの異なるReviewerを持つ仮想的な2 Turnを用意し、それぞれのRevision completed entryが自分のTurnのReviewerだけを参照し、前のTurnの値を引き継がないことを直接確認しています。

将来、per-task／per-revisionで異なるReviewerを許容する設計へ変更される場合（例えば`WorkflowTaskEvidence`へper-task Reviewer IDフィールドが追加される場合）は、本ADRのこの前提が崩れるため、その時点でDirected Communication成立条件を再評価する必要があります。

### @mention可能条件

```go
func (entry ConversationEntry) MentionAllowed() bool {
    return entry.Category == CategoryDirectedCommunication && entry.Speaker != nil && entry.Recipient != nil
}
```

この判定はGo側の`ConversationEntry`メソッドとして公開し、UIが独自に再実装する必要がないようにします。Recipientだけ分かってSpeakerが不明な場合、あるいはその逆の場合は、`Category`が`DirectedCommunication`にならない設計そのものによって、この関数は常に`false`を返します。

### 将来のHTTP JSON contract方針

`MentionAllowed()`は現在Go methodであり、`ConversationEntry`をJSONへ直接serializeしてもこのメソッド自体はフィールドとして現れません。将来Conversation ProjectionをHTTP経由でCursor/UIへ渡す際は、次の**Option B**を採用します。

> **Option B: `mention_allowed` boolをread-model JSONへadditiveに公開する。**

`Category`/`Speaker`/`Recipient`からUI側が独自に`category == "directed_communication" && speaker != null && recipient != null`を再実装するOption Aは採用しません。理由は、UIが同じ判定ロジックを別言語で再実装すること自体が「UIが独自の別ルールを発明しない」という目的に反するためです。Option Bであれば、Go側の`MentionAllowed()`が唯一のtruth sourceのまま、UIは`mention_allowed`というbooleanを読むだけで済みます。この方針は決定として記録するだけであり、**今回のCP3ではHTTP実装を行いません**。

### Deterministic ordering保証

同一canonical evidenceから呼び出した`InspectConversation`は、常に同一順序のentry列を返します。

- entry構築は`record.Turns`／`turn.Workflow.Tasks`／`turn.Answers`／`plan.ProposedTasks`という**slice**の反復だけで行われ、Go mapの反復は一度も順序に影響しません（`employeesByID`は単純なkey lookupにのみ使用）。`os.ReadDir`（Reviewファイル列挙、Vault Adapter既存実装）はGo標準ライブラリの仕様上ファイル名でソート済みの結果を返すため、これも決定的です。
- 最終的な並び替えは`sort.SliceStable`を`At`だけで比較して行います。同一`At`を持つ複数entryは、構築時に追加した順序（Turn順→Turn内はDeliverable→Revision→Review verdict→Task completedという固定コード順）がそのまま保たれます。
- `TestWorkflowTaskConversationEntriesOrderIsFixedForSharedTimestamp`は、同一timestampを共有する複数entryの厳密な順序を20回の反復で固定しています。`TestInspectConversationProjectsFullReviewedWorkflowDeterministically`は、実際のVault-backed統合シナリオ（3 Taskが同一Turnのtimestampを共有）に対し10回の反復でbyte-identicalな出力を確認しています。Goはmap反復順を意図的にrandomizeするため、隠れたmap依存があればこれらの反復testで高確率に露見します。

### Review / Revision projection

`ConversationEntry`は次のtyped factsを持ちます。

```go
type ConversationEntry struct {
    At       time.Time
    Category ConversationCategory
    Kind     ConversationKind

    Speaker   *ActorRef
    Recipient *ActorRef
    Subject   *ActorRef
    Audience  Audience

    TaskTitle string

    ReviewVerdict review.Verdict
    ReviewIssues  []review.Issue
    ReviewSummary string

    DeliverableReference string

    FailureDetails *failure.Envelope

    CEOMessageText string
}
```

既存Domain型（`review.Verdict`、`review.Issue`、`failure.Envelope`）をそのまま再利用し、重複型を作りません。`ReviewIssues`/`ReviewSummary`は`review.Decision`から一切変換せずそのまま転記します（`Issue.Description`/`Issue.SuggestedAction`を含む）。

### completed Japanese messageをGoで作らない理由

`Body string`のような完成済み文章fieldは持ちません。Goが返すのはtyped factsだけです。「伊藤 健太 @佐藤 葵 修正をお願いします。・要件が不足しています。」のような表示は、将来Cursor/UI側が`Speaker`/`Recipient`/`ReviewIssues`から組み立てるpresentation templateの責務です。理由は次の通りです。

1. 日本語文章の組み立てロジックをGo Coreに持たせると、UI表現の変更のたびにGo側の変更が必要になり、Kernel First/Presentation分離の原則に反します。
2. `CEOMessageText`/`ReviewSummary`/`ReviewIssues[].Description`は、CEO自身の入力またはReviewerの既存canonical Decision text**そのもの**の転記であり、WorkCairnが新しく作文したものではありません。これらのpassthroughは許可されますが、それらを結合・整形した文章はGoの責務ではありません。

### read-only保証

`InspectConversation`は次を一切行いません。

- Vault write
- Task write
- Interaction write
- Ledger write
- Provider呼び出し
- LLM呼び出し
- 新しいDomain Event発行

既存の`InspectInteraction`、`InspectOrganization`、`InspectTaskEvidence`、`vault.CommandLedgerStore.Get`だけを呼び出し、いずれもread専用のAdapter経路です。統合テストでは、呼び出し前後のVault snapshotが完全一致することを確認しています。

### canonical evidence不足時はCompanyFactまたはprojectionしない

- Speaker/Recipientの一方が解決できない場合は、Directed CommunicationをCompanyFactへ格下げします（推測でどちらかを埋めません）。
- CEO Message以外でcanonical値が存在しない場合（例: 空文字列のclarification回答）は、そのentry自体を生成しません。
- FailureEnvelopeがCommand Ledgerから取得できない場合、Interaction Sessionへ既に記録済みのbounded summary（`Code`/`Stage`/`Partial`）だけから最小限のEnvelopeを再構成します。新しい診断情報は一切作りません。

### UIがpresentation templateを所有すること

このCheckpointではUIを一切変更しません（`go/internal/httpapi/web/*`、browser testsは対象外）。HTTP API露出も今回は行いません——既存のGo Process contract（`InspectConversation`関数）を確定させることが目的であり、外部公開が必要かどうかは次のCheckpointでUI側の要件と合わせて判断します。

## Consequences

- UIは将来、`Category`/`Speaker`/`Recipient`/`ReviewIssues`等のtyped factsだけを読み、Slackのような会話としてdeterministicに描画できるようになります。
- Domain Eventは変更されず、Article 7の原則を維持したまま新しいpresentation層を追加できました。
- @mentionの安全性判定はGo側の`MentionAllowed()`という単一のtruth sourceに集約され、UI側で再実装・再解釈される余地がありません。
- 「WorkCairn / 任せて進んだ仕事 / 会社が進めたstep: N / 重要な承認: N」のような集計表示は、今回`ConversationEntry`へ一切移植していません。Proof of Work / Autonomy / Audit evidence自体（`WorkReport`等の既存型）は無変更のまま維持されます。UIから集計表示を外す作業は別のCheckpoint（Cursor UI作業）です。
- HTTP API露出は今回行っていません。次のCheckpointでUI側の要件が固まった時点で改めて判断します。
