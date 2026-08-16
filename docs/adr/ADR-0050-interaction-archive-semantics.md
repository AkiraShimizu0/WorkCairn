# ADR-0050: Interaction Archive Semantics

## Status

Accepted

## Context

CEOのInteraction一覧が増えるにつれ、「履歴から削除したい」という要望が見込まれます。しかしInteraction Sessionは、append-onlyなTurn履歴（ADR-0028）・Command Ledgerによるclaim-before-effect（ADR-0021）・FailureEnvelope（ADR-0041）など、監査性・再現性を前提としたDomain設計の中心にあります。ここへ物理削除を持ち込むと、次のいずれかを破壊します。

- 既存Command（`interaction.plan.apply`等）のReplay可能性——削除されたSessionへのReplayは再現不能になります。
- Project/Task/Deliverable/Review/Revisionなど、Sessionから派生した他Domainの証跡との整合性——Sessionだけを消すと、これらの証跡が「どのSessionから生まれたか」を遡れなくなります。
- Command Ledger自体の監査ログ——Sessionへの参照を含むLedger recordは、Session削除後も残り続けます。

そこで本ADRでは、「履歴から削除」をアーカイブ／アンアーカイブ（一覧からの可視性切り替え）として実装し、物理削除は本ラウンドの対象外とします。将来SQLiteへの移行（Round E設計）が行われても、このDomain契約がAdapterの入れ替えだけで成立し続けることを要件とします（`interaction.Store`インターフェースのドキュメントコメントが既に述べる、「DomainとServiceはAdapterがVaultかSQLiteかを知らない」という既存方針の延長です）。

## Decision

### アーカイブは物理削除ではない

`interaction.archive` / `interaction.unarchive`は、Sessionそのもの・Turn履歴・派生Domain（Project/Task/Deliverable/Review/Revision/Command Ledger/FailureEnvelope/Proof of Work）を一切変更しません。変更されるのは「アクティブな一覧に表示するかどうか」という可視性メタデータだけです。

### 正本はTurn履歴——ステートフィールドを追加しない

`interaction.Record`に`Archived bool`のようなフィールドは追加しません。代わりに、既存のTurn Kind体系（`interaction.go`の`TurnKind`定数群）へ`TurnArchived`/`TurnUnarchived`を追加し、アーカイブ操作そのものを新しいcanonical Turnとして記録します。

```go
TurnArchived   TurnKind = "archived"
TurnUnarchived TurnKind = "unarchived"
```

各TurnはKindとAt（記録時刻）だけを持ち、UI向けの理由文言・自由記述テキストはDomainへ一切持ち込みません（Reasonフィールドは存在しません）。

`Record.IsArchived()`は、Turn履歴を末尾から走査し、最も新しい`TurnArchived`/`TurnUnarchived`のどちらであったかを都度導出します。ストアされたbooleanは一切参照しません。

```go
func (record Record) IsArchived() bool {
    for index := len(record.Turns) - 1; index >= 0; index-- {
        switch record.Turns[index].Kind {
        case TurnArchived:
            return true
        case TurnUnarchived:
            return false
        }
    }
    return false
}
```

HTTP層は読みやすさのため、レスポンスへ`archived bool`という計算済みフィールドを追加で載せます（`interactionRecordView`によるstruct embedding）。これは読み取り専用の投影であり、正本は常にTurn履歴です。

### 既存Workflow Stateとは完全に直交

`Record.State`（`StateClarificationRequired`等の既存Workflow状態機械）は、アーカイブ操作によって一切書き換えません。`Record.Validate()`のTurn再生ループ内で、Workflow Stateを追跡する既存の`state`変数とは別に、アーカイブ専用のローカル変数`archived`を新設し、アーカイブ操作の妥当性（正しい順序での出現、他Turn種別のevidenceを一切運ばないこと）だけをこのスコープ内で検証します。`state`側の遷移ロジックへは一行も触れていません。

これにより、途中確認待ち（`StateClarificationRequired`）・承認待ち（`StatePlanApprovalRequired`）・完了済み（`StateReadyToExecute`のワークフロー完了後）など、どのWorkflow StateにあるSessionでも同一の意味でアーカイブ／アンアーカイブできます。

### Command契約

新規Command `interaction.archive` / `interaction.unarchive`を追加しました。Payloadは既存の同種Command（`interaction.plan.generate`等）と同じ最小形です。

```json
{
  "session_id": "...",
  "expected_version": 3,
  "current_time": "2026-08-16T12:00:00Z"
}
```

アーカイブ理由・自由記述フィールドは持ちません（§1の通りDomainへUI文言を持ち込まないため）。

書き込みは既存のCommand Ledger claim-before-effect（Process層`executeInteractionArchiveToggle`）を経由してのみ行われます。専用の新しいREST PATCHエンドポイントは追加せず、既存の`POST /v1/commands`契約をそのまま再利用しています。

### CAS（expected_version）

`expected_version`は必須とし、既存のOptimistic Concurrency（`interactionService.Update(ctx, next, record.Version)`）をそのまま再利用します。アーカイブ専用のCAS経路は作らず、stale versionは既存の`interaction.ErrVersionConflict`/CAS失敗パスへ合流します。

### 冪等性——既にアーカイブ済み／既にアクティブなSessionへの再操作

**決定: 既にアーカイブ済みのSessionへの`interaction.archive`（新しいCommand IDでの再送）、および既にアクティブなSessionへの`interaction.unarchive`は、`ErrInvalidState`による明示的な拒否とし、無言の成功（no-op success）にはしません。**

理由は、既存Domainの一貫した設計方針をそのまま踏襲したことにあります。`interaction`パッケージの既存の全`RecordX`系メソッド——`RecordPlan`・`RecordAnswers`・`RecordApplied`・`RecordWorkflow`・`RecordAction`——は、期待する直前状態でないSessionに対して例外なく`ErrInvalidState`を返す設計であり、そのいずれも「同じ操作を繰り返したら成功扱いにする」というno-op成功パターンを採用していません。今回新設する`RecordArchive`/`RecordUnarchive`だけがこの規約から外れる理由はなく、一貫性を優先しました。

これは「真に同一のリクエストが再送された場合」の冪等性を否定するものではありません。**同一Command IDでの再送に対する冪等性は、Command Ledgerのreplay機構（`replayDurableCommand`）が既に別レイヤーで保証しています**——同じCommand IDでの再送は、`RecordArchive`/`RecordUnarchive`へ二度到達することなく、キャッシュされた終端結果をそのまま返します。したがって、ここで問題になるのは「新しいCommand IDで、既にアーカイブ済み（またはアクティブ）なSessionに対して重複した意図の操作が届いた」場合だけであり、これは既存の`RecordApplied`が「既に適用済みのPlanへ再度Applyしようとするリクエスト」を拒否するのと同じ性質の状態不整合です。呼び出し側（UI/CLI/Recovery）は、この拒否を「既に望む状態にある」というヒントとして扱うことができ、Domain側で黙って握りつぶす必要はありません。

### Process層

`ExecuteInteractionArchive` / `ExecuteInteractionUnarchive`を追加し、既存の他Interaction Commandと同一の骨格（claim → Session読み込み → expected_version検証 → Turn追加 → Store更新 → Ledger終端記録）を共有ヘルパー`executeInteractionArchiveToggle`として実装しています。両者は「どちらのTurnを追加するか」（`interaction.Record.RecordArchive`/`RecordUnarchive`をメソッド式として渡す）と、Command operation名・失敗コード（`INTERACTION_ARCHIVE_FAILED`/`INTERACTION_UNARCHIVE_FAILED`）だけが異なります。

失敗の型付けは、Review・Task実行が採用した`failure.Envelope`（ADR-0041）ではなく、`interaction.plan.apply`・`interaction.answer`など大多数の既存Interaction Commandが使う`(Code, Stage, Partial)`の型付け形式に揃えました。ADR-0041はVertical Slice単位での段階的移行を明示的な方針としており、Interaction系Commandは同ADRの「今回対象外」リストに含まれているため、アーカイブだけを先行してEnvelope化することは、同一Domain内での不整合を新たに生むと判断しました。

### HTTP読み取り契約

`GET /v1/interactions`はデフォルト（クエリなし、または`archived=false`）でアクティブなSessionのみを返します。既存クライアントの挙動は変更されません。

```
GET /v1/interactions            -> archived=false と同じ（後方互換）
GET /v1/interactions?archived=false
GET /v1/interactions?archived=true   -> アーカイブ済みのみ
GET /v1/interactions?archived=all    -> 両方
```

未知の値（例: `archived=bogus`）は`400 INVALID_QUERY_PARAMETER`として拒否します。フィルタリングはHTTPハンドラ層だけで行い、`InteractionInspector`インターフェースや`InspectInteractions`Process関数の契約は変更していません。

### 詳細参照はアーカイブ後も継続可能

`GET /v1/interactions/{id}`・`.../conversation`・`.../work-report`は、対象がアーカイブ済みかどうかに関わらず常に同じ結果を返します。アーカイブは「一覧からの除外」であって「データアクセスの制限」ではないため、これらのハンドラには一切のアーカイブ条件分岐を追加していません。

### Public Beta許可リスト

`interaction.archive` / `interaction.unarchive`を`publicBetaCommandOperations`へ追加しました。両者はProject/Task/Review/Revision/Deliverableのいずれの証跡も変更しないため、他のInteraction系Commandと同等に許可しています。

### Event Busとの関係

現行のEvent Bus（in-process・at-most-once・非durable）を、アーカイブ状態の正本にはしません。将来Business Eventを任意で発行する余地は残しますが、アーカイブ状態は常にInteraction Turn履歴から再構築可能でなければならず、Durable Outboxは本ラウンドでは実装しません。

### Vault Adapter

既存の`interaction.Store`のUpdateパスをそのまま再利用します。アーカイブ専用の新しいファイルや保存形式は作らず、既存のInteraction Record JSON内の`Turns`配列へ追記されるだけです。実VaultへはこのADRの検証中一切書き込んでいません（テストは全て一時ディレクトリ上のVaultのみを使用）。

### JSON Contract

すべて加算的（additive）変更です。

- `interaction.Record`のJSON shapeは無変更——新しいTurn Kind文字列（`"archived"`/`"unarchived"`）が`turns[].kind`の取りうる値へ追加されるだけで、既存フィールドの型・必須性は変わりません。
- HTTPレスポンスの`interactionRecordView`は、既存の`interaction.Record`をstruct embeddingで包み、`archived bool`を新規trailingフィールドとして追加しただけです。既存クライアントが`interaction.Record`としてこのレスポンスをデコードしても、新フィールドは単に無視されます。
- 既存のInteraction JSON（アーカイブ機能追加前に保存されたもの）は、`Turns`にアーカイブ関連Turnが一つも含まれないため、`IsArchived()`は決定的に`false`を返し、マイグレーション不要でそのままデコード・利用できます。

## Consequences

- CEOは「履歴から削除」したSessionを一覧から隠せるようになり、かつ監査証跡・Replay可能性・派生Domain（Project/Task/Deliverable/Review/Revision）との整合性は一切損なわれません。
- アーカイブ状態は常にTurn履歴から導出されるため、daemon再起動やVault再読み込み後も一貫して正しく復元されます。
- 既存Interaction Commandの承認・CAS・Command Ledger claim-before-effect・no-automatic-retryといった規約は、アーカイブにもそのまま適用され、アーカイブ専用の特例パスは一切導入していません。
- 既にアーカイブ済み／既にアクティブなSessionへの重複した意図のCommandは、`ErrInvalidState`により明示的に拒否されます（真の同一リクエスト再送はCommand Ledgerのreplayが別途保証）。
- `interaction.Store`インターフェースにも、Vault Adapter固有の実装にも変更を加えていないため、将来SQLite Adapterへ移行した場合も、このArchive Domain契約（Turn Kind・`IsArchived()`・Command payload・CAS）はそのまま成立し続けます——移行が必要なのはAdapter実装だけです。
- UI変更は本ラウンドの対象外です。アーカイブ／アンアーカイブを実際にCEOへ提示するUI（一覧のフィルタ切り替え、アーカイブボタン等）は別ラウンドの作業として残っています。
- 物理削除（Vaultファイル自体の削除）は依然として本ラウンドの対象外であり、必要になった場合は別ADRとして改めて設計します。
