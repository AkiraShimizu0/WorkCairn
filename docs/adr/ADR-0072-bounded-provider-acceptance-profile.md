# ADR-0072: Bounded Provider Acceptance Profile — Optional, Closed, Session-scoped Execution Bound

## Status

Accepted（PB-3an.2でproduction vertical sliceを実装し、PB-3an.2aのCodex focused reviewで指摘されたP1 4件・P2 2件・P3 1件をPB-3an.2bでfocused correctionした。さらにPB-3an.2cのCodex focused reviewが指摘したtest evidence上のP1 2件を、production codeを一切変更せずPB-3an.2dでtest強化のみにより解消した。`go/internal/autonomy`／`go/internal/interaction`／`go/internal/process`／`go/internal/service`／`go/internal/commandcontract`／`go/internal/httpapi`／Web UI／`tests/browser`すべてに実装・testが存在し、PB-3an.2eのCodex implementation reviewがGOを出したため、StatusはこのCheckpoint（PB-3an.2f）で`Accepted`へ変更した。本ADRは、PB-3ag／PB-3jで観測されたProvider failure根本原因を修正したとも、実Providerでの成功を確認したとも、Public Beta GOに近づいたとも主張しない——それらは本ADRのAcceptedの範囲外である）

本Checkpoint（PB-3an.2）で、これまでのfocused reviewサイクル（PB-3an.1a〜1f）で確定した設計をそのまま実装契約として、production vertical sliceを実装した。設計自体の技術的なP1／P2はPB-3an.1fまでにすべて解決済みだったが、それは「review完了」を意味しない——**Codexが実際のコードに対してGOを出すまで、Statusは`Accepted`へ変更しない**という契約を維持した（設計レビューのGOと実装レビューのGOは別の問いである）。PB-3an.2aのCodex focused reviewは実装自体からP1 4件・P2 2件・P3 1件を指摘し、PB-3an.2bでいずれもsource根拠付きに修正した（詳細は「Implementation Findings Resolved」節）。PB-3an.2cのCodex focused reviewはさらにP1 2件を指摘したが、いずれも実装そのものの欠陥ではなく「実際には保証されていることをtestが十分厳密に証明できていない」というtest evidence側の指摘だった（PB-3an.2dで対応、詳細同節）。PB-3an.2eのCodex implementation reviewは実装・test双方にGOを出し（31ファイルcommit-ready、ADR Accepted昇格可能と判定）、本Checkpoint（PB-3an.2f）でその判定に基づきStatusを`Accepted`へ変更した。「実装した」「testで確認した」「Codex GOを得た」という事実の記述と、「PB-3ag／PB-3jのProvider failure根本原因修正・実Provider成功・Public Beta GOは主張しない」という範囲外事項の記述は、引き続き明確に分けて用いる。

## Context

Codexのfocused reviewは、既存のKeychain-only daemon／HTTP経路が次を構造的に保証できないと診断した（P1-1〜P1-6）。

- Plan生成が厳密に1回であること
- Task作成／実行が厳密に1件・最大1回であること
- Reviewが最大1回であること
- 1 Interaction Session全体でのProvider呼び出しが最大3回であること
- clarification、Plan failure、timeout、Request Changesの各点で確実に停止すること
- Plan再生成、追加Task、追加Review、Revision、Recovery、retry、fallbackがいずれも発生しないこと

既存のBudgetGuard v1（[ADR-0054](ADR-0054-budgetguard-v1.md)）は1 Reviewed Workflow execution scopeの`MaxProviderCalls`をprocess-local `budgetTracker`（`go/internal/service/reviewed_workflow_run_service.go`）で強制するが、次の理由でこの要求を単独では満たせない。

1. **Revision作成はProvider呼び出しを1件も伴わない。** `service.reviser.Execute`（`reviewedChildCommandID(parentCommandID, "revision.execute", taskID)`経由）はTask状態遷移のみで、BudgetGuardが観測する「Provider呼び出し直前」のfire pointを一度も通過しない。したがってBudgetGuardは、Request Changesの後にRevisionが作られること自体を止められない。
2. **Plan生成は別Commandであり、Reviewed Workflowの外側で起きる。** `go/internal/process/interaction.go`の`executeInteractionPlanGenerationCommand`（245行目）は、standalone `interaction.plan.generate`（HTTP executor経由）と`ExecuteInteractionStart`／`ExecuteInteractionAnswer`が`commandledger.DeriveChildCommandID`で導出するchained childの、両方から呼ばれる唯一の実行本体である。この呼び出しはAutonomy Contractがまだ存在しない時点で起きるため、Contract側の`MaxProviderCalls`では原理的にカバーできない。
3. **既存`budgetTracker`はprocess-localである。** `go/internal/service/reviewed_workflow_run_service.go`の`budgetTracker`（594行目付近）はGo processのmemory内state。「process再起動後も3回上限を破れない」という要求には、Session自体の永続表現（Vault上のInteraction Record）に基づく別の会計が必要で、既存BudgetGuardの責務を拡張するのではなく、別レイヤーとして追加する。
4. **並行二重承認。** `ExecuteInteractionPlanApproveAndExecute`（`go/internal/process/interaction_approve_and_execute.go`）の既存claimは、呼び出し側が供給する`input.CommandID`をkeyにしたCommand Ledgerの冪等性しか提供しない。同じ承認対象（同じ`SessionID`＋`ExpectedVersion`＋`PlanDigest`）に対して、二つの異なるbrowserタブ／二重submitが二つの異なる`CommandID`を生成した場合、既存のreplay機構はこれを同一requestとして検出できない。
5. **daemon HTTP allow-listは`interaction.plan.approve_and_execute`だけではない。** `go/internal/httpapi/contract.go`（58〜84行目）は`interaction.start`／`interaction.plan.generate`／`interaction.answer`／`interaction.plan.apply`／`interaction.workflow.execute`／`interaction.plan.approve_and_execute`／`interaction.workflow.recover_revision`／`interaction.archive`／`interaction.unarchive`のすべてを一般daemonから到達可能なoperationとしてexact allow-listしている。一般UIが通常`interaction.plan.approve_and_execute`しか呼ばないという事実は、standalone `interaction.plan.apply`／`interaction.workflow.execute`／`interaction.workflow.recover_revision`が同じHTTP経路から直接呼び出し可能であるという事実を変えない——UI非表示は安全性の根拠にならない。

本ADRは、この6項目の診断へ応える**closed, additive, 既定OFFのbounded_acceptance profile**の契約を提案する。既存のstandard profile（`autonomy.NewStandard`、`Revision: PermissionDelegated`）の挙動・serialized出力は一切変更しない設計である。

## Decision

### 1. Profile型・不変性・生成契約

- 新しいclosed文字列型`interaction.Profile`を`go/internal/interaction`パッケージへ追加する。

  ```go
  type Profile string

  const (
      ProfileStandard          Profile = ""
      ProfileBoundedAcceptance Profile = "bounded_acceptance"
  )

  func (profile Profile) Valid() bool {
      return profile == ProfileStandard || profile == ProfileBoundedAcceptance
  }
  ```

- `interaction.Record`へ`Profile Profile`フィールド（`json:"profile,omitempty"`）を追加する。`ProfileStandard`は空文字列のGo zero valueであるため、`omitempty`により既存standard SessionのJSON出力とbyte-for-byte同一のまま——既存fixture／golden JSONを一切壊さない。
- **`Record.Validate()`自身がclosed-value guardを持つ。** `go/internal/interaction/interaction.go`の`Record.Validate()`（298行目以降）冒頭の一括条件（`if record.SchemaVersion != SchemaVersion || !sessionIDPattern.MatchString(record.SessionID) || ... || record.Turns == nil { return ErrInvalidSession }`、300〜306行目）へ、`|| !record.Profile.Valid()`を1条件追加する。`Profile.Valid()`は`ProfileStandard`（空文字列）と`ProfileBoundedAcceptance`の2値だけを真とする（1節冒頭の定義どおり）ため、**それ以外のいかなる文字列（typo、将来値、bit rot等）を持つRecordも`Validate()`自体がfail-closedで拒否する**——unknown値をstandardへ暗黙に退化させたり、bounded相当として扱ったりしない。
  - **この1箇所の追加だけで、Recordが生まれる／読み書きされるすべての経路が自動的にこのguardを通る。** `NewWithProfile`は末尾で`record.Validate()`を呼ぶ（1節末尾のコード参照）ため新規作成時に効く。加えて、`go/internal/adapter/vault/interaction_store.go`を確認したところ、**JSON／Storeとの往復もすべて既存の`Validate()`呼び出しを既に経由している**——読込側は`InteractionStore.Get`（71〜78行目）が呼ぶ`store.read`が、JSON decode直後に`record.Validate() != nil`を拒否条件へ含む（167行目付近）。書込側は`encodeInteractionRecord`（179行目）が、`json.MarshalIndent`する**前**に`record.Validate() != nil`を拒否条件としている。したがって、`Record.Validate()`へこの1条件を追加するだけで、**新規作成・Vaultからの読込・Vaultへの書込のいずれの経路でも、未知のProfile値を持つRecordは通過できない**——Storeレイヤーやconstructor個別に重複したvalidationを追加する必要はない。
  - **`Record.Validate()`と`ValidateTransition`は役割が異なる。** `Record.Validate()`のこの新しい条件は「このRecord単体が持つProfile値が、そもそも認識済みのclosed値であるか」という**型レベルの妥当性**を保証する（毎回のload／construct／save全てで検証される）。一方、既存の`ValidateTransition`（927〜939行目）へ追加する`current.Profile != next.Profile`（後述）は「（両方とも既にvalidな）2つのRecordの間で、Profileという不変fieldの値が変わっていないか」という**不変性**を保証するもので、Session更新（`interactionService.Update`によるCAS commit）の瞬間だけに働く別の検証である。前者が欠ければ壊れた値を持つRecordがそもそも生き残ってしまい、後者が欠ければ一度有効な値で始まったSessionが後から値をすり替えられてしまう——どちらか一方では他方を代替できない、意図的に独立した2つのguardである。
- **`interaction.New(sessionID, request, model string, createdAt time.Time) (Record, error)`のシグネチャ・挙動は一切変更しない。** 常に`ProfileStandard`のRecordを返す既存の唯一の一般constructorとして維持する。
- 新しい**検証済み**constructorを追加する。「`interaction.New()`後にexported fieldを直接代入する」という前回案は削除し、`Record`はconstructor経由でのみProfileを持って生まれる契約へ改める。

  ```go
  // NewWithProfile is New plus an explicit, validated Profile selection. It
  // delegates entirely to New for every existing invariant (SessionID
  // pattern, request/model shape, RequestDigest) and only additionally
  // requires profile.Valid(). New itself is unchanged and remains the
  // standard-profile entry point every existing caller keeps using.
  func NewWithProfile(sessionID, request, model string, profile Profile, createdAt time.Time) (Record, error) {
      record, err := New(sessionID, request, model, createdAt)
      if err != nil {
          return Record{}, err
      }
      if !profile.Valid() {
          return Record{}, ErrInvalidSession
      }
      record.Profile = profile
      return record, record.Validate()
  }
  ```

  `New`自身は1行も変更しない。`NewWithProfile`は`New`の戻り値へ委譲するadditiveな薄いラッパーであり、`record.Profile = profile`という代入は**この関数の内部**（まだ誰にも返していない、たった今`New`から返ってきたローカル値）に閉じる——外部呼び出し元が`New`の戻り値を受け取った後に直接fieldを書き換える、という前回の設計は撤回する。一般UIの新規依頼開始経路（`ExecuteInteractionStart`）は`New`ではなく`NewWithProfile`を呼ぶよう変更する。既存の`New`の他の呼び出し元（テスト、CLI等、standard固定）は無変更のまま。
- **不変性はValidateTransitionで強制する。** `go/internal/interaction/interaction.go`の`ValidateTransition`（927〜939行目）は既に`current.SessionID != next.SessionID`／`current.RequestDigest != next.RequestDigest`を不変性違反として拒否している。ここへ`current.Profile != next.Profile`を1条件追加する——`Record`の他の不変fieldと全く同じ場所・同じ形の強制であり、新しい検証機構を発明しない。
- **既存Session digest（`interaction.Record.RequestDigest`）とCommand request digest（`claimWorkspaceCommand`が計算する`interaction.start`自身のrequest digest）は別物であり、扱いが異なる。**
  - **Session digest（`RequestDigest`）へはProfileを混ぜない。** `requestDigest`（955行目）は`SessionID`／`Request`／`Model`／`CreatedAt`だけをhashしており、`Profile`を含めない——`NewWithProfile`で`ProfileBoundedAcceptance`を指定してもRequestDigestの値はstandardと同じ入力に対して変わらない。これは意図的な設計判断である：`RequestDigest`はCEOが打った自然言語requestそのものの改変検知（ADR-0028の「immutable requestと承認対象digestに拘束されたsession evidence」）のためのfieldであり、Profileという実行方法の選択はrequestの内容そのものではないため、この既存fieldの意味を変えない。
  - **一方、`interaction.start`のouter Command claimが計算するrequest digest（`go/internal/process/interaction.go:159〜164`の匿名struct）へは`Profile`を含める。** 現状のstructは`{SessionID, RequestDigest, Model, CurrentTime}`の4 fieldのみ。ここへ`Profile string \`json:"profile,omitempty"\``を追加した5 field structへ拡張する。

    ```go
    claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.start", candidate.SessionID, struct {
        SessionID     string    `json:"session_id"`
        RequestDigest string    `json:"request_digest"`
        Model         string    `json:"model"`
        Profile       string    `json:"profile,omitempty"`
        CurrentTime   time.Time `json:"current_time"`
    }{candidate.SessionID, candidate.RequestDigest, candidate.Model, string(candidate.Profile), candidate.CreatedAt})
    ```

    `Profile`は既存4 fieldの後（`CurrentTime`の前）に追加し、`omitempty`とする。`ProfileStandard`（空文字列）のときはJSON出力からfieldそのものが消えるため、**standard requestのCommand request digestは本変更の前後でbyte-for-byte同一**——既存のstandard `interaction.start`呼び出し元・既存test・既存replay挙動に一切影響しない。
  - **同じCommand ID・同じrequest・同じmodelでも、standardとboundedは異なるCommand request digestになる。** `claimWorkspaceCommand`は`commandledger.RequestDigest(value)`（`go/internal/commandledger/ledger.go:170`）でこの構造体全体をhashしてCommand Ledgerの`RequestDigest`fieldへ保存し、既存の「同一Command IDに異なるpayloadが再度渡されたら"異なるrequest"としてconflict拒否する（`ROADMAP.md`「Idempotency and Command Ledger Foundation」に記載の既存契約）」という一般Command Ledger規則がそのまま適用される。ゆえに、同じ`CommandID`に対して一度standard（`Profile`省略）で`interaction.start`をclaimした後、同じ`CommandID`でbounded（`Profile: "bounded_acceptance"`）として再送すると、既存のrequest digest不一致検出により「別のrequest」としてreplayではなくconflictで拒否される——replay時にProfileだけがすり替わることはない。
  - **この2ケース（standard requestのdigest非変化、standard/bounded間のdigest相違による拒否）をtestで必須化する**（10節のtest matrixに明記）。

### 2〜3. Plan reservation（Session全体で厳密1回、正式経路すべてに閉じる）

**前回案の`PlanGenerationReserved bool`は削除する。** 代わりに、既存の`Turns []Turn`／`Version = len(Turns)+1`というappend-only構造（`interaction.go`301〜304行目で`record.Version != uint64(len(record.Turns))+1`が既に検証されている）へ、新しいTurn種別として組み込む。

```go
const TurnPlanGenerationReserved TurnKind = "plan_generation_reserved"

// Turn へ追加するfield（他のKind専用fieldと同じ形）
ReservedChildCommandID string `json:"reserved_child_command_id,omitempty"`
```

`Record.Validate()`（298〜424行目のTurn再生ループ）へ新しいcaseを追加する。

```go
case TurnPlanGenerationReserved:
    if record.Profile != ProfileBoundedAcceptance || state != StatePlanGenerationApprovalRequired ||
        planGenerationReserved || turn.Plan != nil || turn.PlanDigest != "" || len(turn.Answers) != 0 ||
        turn.ProjectID != "" || turn.ProjectName != "" || turn.Workflow != nil || turn.Action != nil ||
        commandledger.ValidateCommandID(turn.ReservedChildCommandID) != nil {
        return ErrInvalidSession
    }
    planGenerationReserved = true
    // state is left unchanged (StatePlanGenerationApprovalRequired): this
    // Turn records that an attempt is authorized, not its outcome.
```

（`planGenerationReserved := false`をループ冒頭のローカル変数として追加。`answeredCount`／`archived`と同じ既存スタイル。）このcaseにより、**standard profile（`record.Profile != ProfileBoundedAcceptance`）ではこのTurn種別自体が常に不正**となり、standard Sessionはこの機構に一切触れない。bounded Sessionでは、この種別のTurnは生涯を通じて**高々1回**しか正当化されない（`planGenerationReserved`が既にtrueなら2つ目は`ErrInvalidSession`）。

**Cross-kind validation：`ReservedChildCommandID`は`TurnPlanGenerationReserved`だけが持てる。** 既存の8つのTurn kind（`TurnPlanGenerated`／`TurnClarificationAnswered`／`TurnPlanApplied`／`TurnWorkflowRecorded`／`TurnActionRecorded`／`TurnArchived`／`TurnUnarchived`／`TurnRevisionRecoveryStarted`）は、いずれも自分に無関係な既存field（`turn.Plan`、`turn.ProjectID`等）を明示的にゼロ値強制する既存パターンを持つ（例：`TurnArchived`のcase、393〜398行目は`turn.Plan != nil || turn.PlanDigest != "" || ... || turn.PreAuthorizedWorkflowCommandID != "" || turn.Workflow != nil || turn.Action != nil`を拒否条件に含む）。この既存パターンへ、新しい`turn.ReservedChildCommandID != ""`を**8つのcaseすべての拒否条件へ追加**する。これにより、`TurnPlanGenerationReserved`以外のいかなるTurnも`ReservedChildCommandID`を持てず、`TurnPlanGenerationReserved`自身は（上記caseの`commandledger.ValidateCommandID(turn.ReservedChildCommandID) != nil`により）必ず有効な形式のCommand IDを持たなければならない——空・不正形式のいずれも拒否される。

**Version順序（v1→v2→v3）**：

1. **v1**: `NewWithProfile(...)`で作成（`Version=1`、`Turns=[]`）。
2. **v2**: `executeInteractionPlanGenerationCommand`（`go/internal/process/interaction.go:245`、standalone `interaction.plan.generate`とchained呼び出し両方が経由する唯一の実行本体）が、既存の`record, err := interactionService.Get(...)`（271行目）と既存preflight（272行目）の直後、**実際のProvider呼び出し（`GenerateCEOPlan`、282行目）より前**に、bounded Sessionだけ次を行う。
   - `record.Profile == ProfileBoundedAcceptance`かつ既に`TurnPlanGenerationReserved`が存在する場合：その`ReservedChildCommandID`が今回の`commandID`（この関数の引数、standaloneならCEO承認済みID、chainedならdeterministic child ID）と一致しなければ、Provider呼び出しを一切行わずtyped precondition failureで即returnする。一致する場合は既存のcommand ID replay（`claimWorkspaceCommand`／`replayDurableCommand`、254〜266行目）が既にこのケースを処理済みのため到達しない。
   - 存在しない場合：`record.RecordPlanGenerationReservation(commandID, currentTime)`（新しいRecordメソッド、`TurnPlanGenerationReserved`を1件appendするだけの`RecordPlan`と対称な薄いメソッド）でVersion 2のRecordを作り、`interactionService.Update(ctx, reserved, record.Version)`でCAS commitする。**この時点でreservationは、直後のProvider呼び出しが成功しようが失敗しようがtimeoutしようがprocessがcrashしようが、恒久的にVaultへ残る。**
   - この後、`record`ローカル変数をcommit後の値（Version=2）へ差し替えてから、既存の`request, err := record.PlanningRequest()`／`GenerateCEOPlan(...)`（278〜285行目）を実行する。
3. **v3**: 既存の`next, err := record.RecordPlan(...)`／`interactionService.Update(ctx, next, record.Version)`（295〜305行目、無変更）が、**v2のRecord・v2のVersionを基準に**（ステップ2で差し替えた`record`を使うため自動的にそうなる）、Plan成功時は`TurnPlanGenerated`を、Plan validation failure時は既存どおりcommitしない（`finishInteractionPlan`が`commitErr`／`err`を記録するだけ）を行う。

- **成功・failure・timeout・crashに関係なく新しいattemptを拒否する**：v2のreservation Turnが存在する限り、ステップ2の1つ目の分岐（`ReservedChildCommandID`不一致）が働く。Provider呼び出しがtimeoutしてもcrashしても、v2は既にcommit済みであり、次回同じ操作（standaloneでもchainedでも、同じ関数を通る限り）は必ずこの分岐に入る。「成功したTurnだけを根拠にしない」という要求どおり、v3（`TurnPlanGenerated`）の有無ではなく、v2（`TurnPlanGenerationReserved`）の有無で判定する。
- **Provider呼出しはreservation Turnに保存されたexact child Command IDだけが行える**：ステップ2の一致チェックがこれを保証する。同じSessionに対する異なる`CommandID`（新しいouter `interaction.start`／`interaction.answer`のretryが導出する異なるchild ID、または別のstandalone呼び出し）は、既に別のIDでreservation済みなら必ず拒否される。

**standalone `interaction.plan.generate`は自動的にカバーされる**：`executeInteractionPlanGenerationCommand`はstandalone HTTP operationとchained継続の両方が呼ぶ唯一の実行本体であるため、ここ1箇所へガードを置くだけで、要求3の「standalone `interaction.plan.generate`」は個別の実装を要さずに閉じる。

**`interaction.answer`は別途、より早い時点で拒否する**：`ExecuteInteractionAnswer`（`interaction.go:670`）自身が、Session読取直後・`record.RecordAnswers(...)`を呼ぶ**前**に、`record.Profile == ProfileBoundedAcceptance`なら常にtyped precondition failureで拒否する。bounded Sessionが`StateClarificationRequired`へ到達するのは、唯一許された1回のPlan生成がCEOQuestionsを伴って成功した場合だけであり、そのSession全体にとってこれは構造的な行き止まりである（回答を記録しても、その後に続くPlan再生成の試みは上記v2ガードでどのみち拒否されるが、Answer Turn自体を無意味に記録させないため、`ExecuteInteractionAnswer`の入口で先に閉じる）。

**bounded profileにおける`Next()`（前回「変更不要」としたが撤回する）**：`interaction.go:765`の`Next()`は、`StatePlanGenerationApprovalRequired`（776〜778行目）で無条件に`NextApprovePlanGeneration`／`"interaction.plan.generate"`を、`StateClarificationRequired`（779〜793行目）で無条件に`NextAnswerClarifications`／`"interaction.answer"`を返す——これはbounded Sessionに対しても現状は無条件であり、UIやProcess層のガードだけに頼るなら「一覧としては引き続きanswer／generateを提示してしまう」という状態になる。要求どおり、`Next()`自身がbounded Sessionに対してはこの2状態でanswer／generate／regenerateを一切提示しないよう変更する。

- `case StatePlanGenerationApprovalRequired:`へ、`record.Profile == ProfileBoundedAcceptance`かつ`record.Turns`に既に`TurnPlanGenerationReserved`が存在する場合（＝唯一許された1回のattemptが既に予約済みだが、`TurnPlanGenerated`が後続していない＝失敗／timeout／crash中）は、`NextApprovePlanGeneration`を返さず、代わりに**既存の`NextInspectWorkflow`**（新しい`NextActionKind`は発明しない）を`Operation`・`ApprovalRequired`を空／falseのまま返す。reservation Turnの`ReservedChildCommandID`を`next.Commands = []CommandReference{{Scope: "workspace", CommandID: reservedChildCommandID}}`として設定し、人間がそのCommandの実際の結果を確認できるようにする——`NextInspectWorkflow`は既存コード（811〜819行目）でも「既に確定した何かがある、承認をもう一度求めない、参照Commandを示す」という同じ用途で使われている既存の汎用kindであり、字面が「workflow」でも本来Interactionのnext-action全体における「承認は求めず既存の何かを指し示す」役割を担っている——この既存の役割をPlan生成stageへ転用する。
  - 予約がまだ存在しない場合（bounded Sessionの最初の試行前）は、既存どおり`NextApprovePlanGeneration`／`"interaction.plan.generate"`を返す——最初の1回はstandardと同じ提示のまま。
- `case StateClarificationRequired:`へ、`record.Profile == ProfileBoundedAcceptance`の場合は常に（reservationの有無を問わず）`NextAnswerClarifications`を返さず、同じ`NextInspectWorkflow`（`Commands`は空——この時点でこのSessionにとって参照すべき既存Provider呼び出しCommandは、既に成功して`TurnPlanGenerated`まで進んでいるため、reservation自体を参照する意味がない）を返す。bounded Sessionにとってこの状態への到達自体が「唯一の1回を使い切った」ことを意味するため、reservationの有無で分岐する必要はない。
- 上記どちらの分岐も、`record.Profile == ProfileStandard`のときは一切通らない——**standard profileの`Next()`出力は本変更の前後で1バイトも変わらない**。
- **HTTP projectionとbrowser UIも同じinspect-only状態を表示する**：`Next()`の戻り値をそのままJSON化するHTTP応答（既存の`GET`／pollingエンドポイント）は、コード変更なしに新しい`NextInspectWorkflow`／空`Operation`の組み合わせをそのまま返す。`web/app.js`側は、既存の「`NextInspectWorkflow`かつ`Operation`が空」の表示分岐（既存コードに同種の分岐がある前提——`renderWorkflowApproval`等の既存レンダリング関数が`next.Operation`の有無で分岐している、10節のUI実装で確認）へ自然に落ちるため、Plan生成stage向けの新しいUI分岐を追加する必要はない。固定文言（10節）だけをこの画面へも表示する。

**残る正式経路のfail-closed**（要求3、`interaction.workflow.execute`／standalone `interaction.plan.apply`／`interaction.workflow.recover_revision`）：

`go/internal/httpapi/contract.go`（58〜84行目）が一般daemonへexact allow-listしている`interaction.*` operationを全数確認した結果、Plan生成またはWorkflow実行の副作用を持つのは次の6つに限られる：`interaction.start`／`interaction.plan.generate`／`interaction.answer`／`interaction.plan.apply`／`interaction.workflow.execute`／`interaction.plan.approve_and_execute`。加えて`interaction.workflow.recover_revision`（84行目）がRevision再開に絡む。残る`interaction.archive`／`interaction.unarchive`／`interaction.action.wordpress.publish`はPlan／Task／Provider効果を一切持たないため対象外——これが要求3の「その他source上存在する」に対する網羅的な答えである。

bounded Sessionにとって、CEOの実行経路は`interaction.plan.approve_and_execute`（4節）**だけ**である。standalone `interaction.plan.apply`、standalone `interaction.workflow.execute`、`interaction.workflow.recover_revision`は、bounded Sessionに対しては安全機構を複製して再実装するのではなく、**Session読取直後に無条件でtyped precondition failureとして拒否する**（`record.Profile == ProfileBoundedAcceptance`である限り、Session state・Version・digestを問わず拒否）。`interaction.workflow.recover_revision`はそもそもRevisionを前提とする操作であり、bounded profileは`Revision: PermissionForbidden`（6節）のためRevisionが存在し得ず、拒否は既存の状態と矛盾しない。

`PlanInteractionWorkflow`とAutonomy Contractの解決は、**caller-supplied Contractではなくserver側でSession Profileから導出する**（詳細は6節）。したがって、仮にstandalone `interaction.workflow.execute`の入口拒否を将来誰かが誤って外したとしても、Contract自体はSessionのProfileから再導出されるため、呼び出し側が独自のstandard Contractを渡して制限を迂回することはできない——UI非表示にも入口チェック1箇所にも依存しない二重の安全設計である。

### 4. Approval execution reservation（`interaction.plan.approve_and_execute`）

`ExecuteInteractionPlanApproveAndExecute`（`go/internal/process/interaction_approve_and_execute.go`）の処理順序を次のとおり固定する。

1. **outer Command claim／既存outer replay判定**（既存、無変更）：`claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.plan.approve_and_execute", ...)`（72〜79行目）と`replayDurableCommand`（83〜85行目）。**同一outer Command IDによるreplayは、下記2つのreservationに一切触れずに既存のterminal outer resultをそのまま返す**——`replayDurableCommand`はreservationの状態を読まない、既存の独立した機構のまま。
2. **Session読取**（既存、無変更）：`record, err := interactionService.Get(...)`、`plan, digest, hasPlan := record.CurrentPlan()`（91〜92行目）。
3. **Profile／Version／Plan digest／Task数1件のpreflight**（既存preflight93〜99行目へ、Task数チェックを同じ判定式へ統合する）：

   ```go
   if err != nil || record.Version != input.ExpectedVersion || record.State != interaction.StatePlanApprovalRequired ||
       !hasPlan || digest != input.PlanDigest ||
       (record.Profile == interaction.ProfileBoundedAcceptance && len(plan.ProposedTasks) != 1) {
       // 既存の finishDurableCommand(..., "interaction_preflight", false) へ合流
   }
   ```

   Planの自動修正・再生成は行わない。
4. **deterministic approval reservation claim**（新規）：`record.Profile == ProfileBoundedAcceptance`のときだけ、次で決定的な予約Command IDを計算する。

   既存の`DeriveChildCommandID(parentCommandID, aggregateID string)`（`go/internal/commandledger/ledger.go:181〜189`）は「実在するparent Command IDを親として子を導出する」という意味論を持つ既存関数であり、実在しないparentを表す固定文字列を渡すのは意味論の誤用になる。前回案の可変長`parts ...any`も、要素の型・順序が呼び出し側の裁量に委ねられ、encoding方法が固定されない（例えば素朴な文字列連結では`["ab","c"]`と`["a","bc"]`が衝突し得る）ため撤回する。代わりに、**closedな型付き入力structを持つ専用primitive**を`commandledger`パッケージへ新設する。

   ```go
   // ApprovalReservationID is the closed, versioned input to
   // DeriveApprovalReservationID. Field order, field types, and the JSON
   // encoding they produce are all fixed by this struct's own declaration
   // -- adding, removing, reordering, or retyping a field changes every
   // existing reservation ID and requires introducing a new domain
   // separator (bump the "v1" suffix below), never silently reusing "v1"
   // with a different shape.
   type ApprovalReservationID struct {
       SessionID       string `json:"session_id"`
       ExpectedVersion uint64 `json:"expected_version"`
       PlanDigest      string `json:"plan_digest"`
       Profile         string `json:"profile"`
   }

   // approvalReservationIDDomainSeparator scopes this primitive's hash
   // input to exactly this typed struct/version, so a value that happens
   // to canonical-JSON-encode the same way for an unrelated purpose can
   // never collide with a reservation ID.
   const approvalReservationIDDomainSeparator = "workcairn.approval-reservation.v1"

   func DeriveApprovalReservationID(input ApprovalReservationID) (string, error) {
       if ValidateSessionID... /* whichever existing session/digest validators already apply to each field, PB-3an.2 to wire precisely */ {
           return "", ErrInvalidRecord
       }
       encoded, err := json.Marshal(input) // fixed field order = struct declaration order
       if err != nil {
           return "", err
       }
       digest := sha256.Sum256(append([]byte(approvalReservationIDDomainSeparator+"\x00"), encoded...))
       return "RESERVE-" + hex.EncodeToString(digest[:16]), nil
   }
   ```

   `"RESERVE-"`という既存`"CHILD-"`（`DeriveChildCommandID`専用）とは異なる新しいprefixを使い、既存の`commandIDPattern`が要求する形式（既存`DeriveChildCommandID`の出力と同じ`prefix + hex`の形）を満たす。`json.Marshal`による構造化encoding（各fieldが型付きでquoteされる）が、素朴な文字列連結が持つ境界曖昧性（`["ab","c"]`対`["a","bc"]`型の衝突）を構造的に排除する——`SessionID`・`PlanDigest`・`Profile`のいずれも自由文字列だが、JSON encodingでは相互のfield境界がquoteとfield名で明示されるため、値の内容だけで別のfield境界を偽装できない。

   ```go
   reservationID, err := commandledger.DeriveApprovalReservationID(commandledger.ApprovalReservationID{
       SessionID: input.SessionID, ExpectedVersion: input.ExpectedVersion,
       PlanDigest: input.PlanDigest, Profile: string(interaction.ProfileBoundedAcceptance),
   })
   ```

   `reservationID`を`claimWorkspaceCommand`（workspace scope、新しいoperation名`"interaction.plan.approve_and_execute.reservation"`）でclaimする。
5. **effect開始前にreservationを一方向terminal `consumed`へfinish**（新規）：reservation claimが成功した直後、`ExecuteCEOPlanApply`など実際のeffectを一切呼ぶ**前**に、既存の`finishDurableCommand(ctx, reservationClaim, ReservationResult{Consumed: true}, nil, "", "", false)`パターン（`interaction_approve_and_execute.go:177`の成功終了と同じ形）で、このreservation Command自体を即座に成功terminal（`consumed`）として確定する。この終端値は**その後の`ExecuteCEOPlanApply`／Workflowの成否と無関係**——「この承認スロットは使用された」という一度きりの事実だけを表す。
6. **Project／Task／Provider effect**（既存、無変更）：`ExecuteCEOPlanApply`以降、既存コードをそのまま実行する。

- **異なるouter Command IDでも同じ承認対象は同じreservation keyになる**：`reservationID`は`input.CommandID`に依存しない、`{SessionID, ExpectedVersion, PlanDigest, Profile}`だけの純粋関数（4節）。二つの異なるbrowserタブが異なる`input.CommandID`で同じ対象を承認しようとしても、両者は同じ`reservationID`を計算し、同じCommand Ledgerエントリへ`claim`しようとして競合する。
- **同じouter Commandのreplay（ステップ1・2）はreservationへ一切到達しない**：`replayDurableCommand`（ステップ2）が既存のterminal outer resultを返して即returnするため、同一`input.CommandID`による2回目以降の呼び出しは、ステップ3以降・reservation claim（ステップ4）まで到達すらしない。これはoutre Commandの既存replay機構がそのまま持つ性質であり、reservationの状態には一切依存しない。
- **reservation claimが`running`ならtyped non-success**：ステップ4のclaimが「別のouter Commandが同じ`reservationID`を今まさに処理中（`running`）」を検出した場合、`ExecuteInteractionPlanApproveAndExecute`はtyped non-success errorを返し、`ExecuteCEOPlanApply`以降には一切進まない。
- **terminal reservationが`claim.replay != nil`として返っても、成功replayとして扱わない**：`claimWorkspaceCommand`の一般的な仕組みは、既にterminalなCommand IDを再度claimしようとすると、その既存terminal resultを「replay」として返せるようになっている。しかし`reservationID`のterminal recordは「別の（多くの場合、別のouter Command IDが起こした）試行によってこの承認スロットが既に`consumed`された」という事実を表すだけであり、**今回のouter Command自身がこの結果を生成したわけではない**。したがって、reservation claimがterminal `consumed`を検出した場合、それを無条件で「replayなので成功として返す」という一般パターンへ流用せず、必ず**新しいtyped non-success error**（例：`ErrApprovalReservationAlreadyConsumed`）へ変換したうえで`ExecuteInteractionPlanApproveAndExecute`から返す。
- **新しいouter Commandは、その拒否結果でterminal finishし、runningのまま残さない**：ステップ4のreservation claimが（running中・consumed後いずれの理由であれ）失敗した場合、outer Command自身のCommand Ledgerエントリ（ステップ1でclaim済み）を、この失敗を`commandErr`として渡す既存の`finishDurableCommand(ctx, claim, result, err, code, stage, false)`パターンで**即座にterminal（非success）へfinishする**。outer Commandを`running`のまま放置しない——reservation claimの失敗は、outer Command自身にとっても確定的な最終結果である。
- **winnerのreservation terminal recordとouter Command resultを混同しない**：ステップ4のreservation claimに成功したwinnerは、ステップ5でreservation自体を`{Consumed: true}`という最小payloadでterminal化するが、これは**outer Commandの戻り値ではない**。outer Command自身の実際の結果（`InteractionApproveAndExecuteResult`）は、この後のステップ6（`ExecuteCEOPlanApply`以降）が生成する既存の値のままであり、reservationのterminal payloadをそのまま転用・代入しない——2つのCommand Ledgerレコード（`"interaction.plan.approve_and_execute"`という名前空間のouter Commandと、`"interaction.plan.approve_and_execute.reservation"`という別名前空間のreservation）は最後まで独立した別個のrecordのままである。
- **crash位置ごとの状態**：
  - **ステップ5（`consumed`への即時finish）の前でcrash**：reservation Commandは`running`のまま残る。既存のCommand Ledger `running`検出により、後続の同一対象への新しいouter Commandはブロックされる（fail-closed）。自動resumeやtimeoutによる自動解放は追加しない——Recovery検査でのみ解消する。
  - **ステップ5の後・ステップ6（実際のeffect）の前でcrash**：reservationは既に`consumed`。後続の新しいouter Commandはreservation claimで即座に拒否される。**しかしProject／Taskはまだ作られていない**——このSessionはこの時点で永続的にブロックされたまま、Human Recoveryの明示判断を待つ（bounded profileは自動resumeを追加しない）。
  - **ステップ6（effect開始後）でcrash**：reservationは`consumed`のまま、outer Command自身のCommand Ledgerが`running`または部分的terminal状態を持つ。既存のRecovery機構（`recovery-inspect`）がTask／Project side effectの状態を診断する対象となるが、自動Recovery・resume・rollbackは一切追加しない。新規実行（新しいreservation・新しいTask作成）は恒久的に拒否される。
- **reservation Commandは通常のRecovery候補や誤ったHuman actionを生成しない**：`go/internal/attention`（ADR-0065、Company Attention Feed）の既存4 typeはすべて`interaction.Record.State`／`Next()`または`InspectRoutineScheduleHealth`だけを分類しており、Command Ledgerを汎用的に走査しない（`docs/Architecture.md`「Go Company Attention / Decision Feed」参照）。reservation自体もSessionの`State`を変更しないため、Attention Feedにもinteraction `Next()`にも一切現れない。また`recovery-inspect`（ADR-0020）はTask／Deliverable／Audit中心のevidence走査であり、Command Ledgerの汎用一覧ではない。したがってreservation Commandは、いずれの既存Human向け一覧にも独立した項目として出現しない——出現するのは、Project／Task effectが実際に作られた場合の既存Task／WorkflowのRecovery対象としてのみであり、これは既存のRecovery設計をそのまま踏襲する（新しい分類ロジックを追加しない）。

### 5. Session全体最大3 Provider attempts

- **Plan attempt**：2〜3節のreservation Turnにより、bounded Session全体で最大1回。
- **Task実行＋Review**：6節のbounded Autonomy Contractの`MaxProviderCalls=2`により、既存の`FixedBudgetPolicy`／`budgetTracker`が最大2回を強制する。
- **budget reservationがattemptを消費することの根拠**：`go/internal/service/reviewed_workflow_run_service.go`の`budgetTracker.reserveProviderCall`（625〜631行目）の既存doc commentは次のとおり明記している——「reservation happens before the call; recording happens after」（`recordUsage`のdoc comment、645〜647行目）。`reserveProviderCall`は`tracker.providerCallCount`を、実際のProvider呼び出しの成否を待たずにincrementする（629行目`tracker.providerCallCount++`は呼び出し前に実行され、Provider呼び出し自体の成功・失敗はその後に判明する）。つまりcounterは「試みたか」を数えており「成功したか」を数えていない——「attemptを消費する」という要求はこの既存実装が既に満たしている。
- **daemon crash後もapproval reservationにより別workflow開始を拒否する**：4節のreservationは`interaction.plan.approve_and_execute`という唯一の実行入口自体をSession単位でgateするため、daemonがcrash後に再起動しても、同じ承認対象への新しい試みは同じ`reservationCommandID`に衝突し、`ExecuteCEOPlanApply`／Reviewed Workflowを再び開始できない。
- **bounded profileはRecovery経路を全面拒否する**：3節で`interaction.workflow.recover_revision`を無条件拒否、6節で`Revision: PermissionForbidden`によりRevision自体が作られない（7節）。Budget Recovery Continuation（ADR-0055）はstopしたRevisionの再開機構であり、Revisionが存在しないbounded profileでは到達しない。
- **合計最大3の保証**：Plan attempt最大1（reservation Turn、durable、process再起動を跨いで有効）＋Task＋Review最大2（BudgetGuard、1 Reviewed Workflow execution scope）＋承認自体の二重実行は不可（approval reservation）＋標準経路以外の全standalone入口は拒否（3節）＋crashからの自動再開なし（4節・Recovery全面拒否）＝Session全体を通じて、成功・失敗を問わずProvider呼び出しは最大3回に固定される。

### 6. 1 Task Preflight／Autonomy Contract

- 3節で`interaction.plan.approve_and_execute`のpreflight（3節ステップ3）へ統合済み：canonical Planが厳密に1 TaskであることをApply前に検証する。
- `go/internal/autonomy/contract.go`の`Contract.Validate()`（137〜162行目）の`contract.Revision != PermissionDelegated`を、`contract.Revision != PermissionDelegated && contract.Revision != PermissionForbidden`へ限定的に緩和する。他の4つのPermission field（`TaskExecution`／`Review`／`ExternalPublish`／`Spending`）の固定値要求、`MaxParallelTasks`／`MaxRevisionCount`／`MaxProviderCalls`／`MaxRuntime`のceiling検査は一切変更しない。
- 新しい`func NewBoundedAcceptance(employeeIDs, models []string, maxTasks int) (Contract, error)`を追加する。実装は「既存`NewStandard(employeeIDs, models, maxTasks)`を正規経路で呼び、その戻り値の`Clone()`にだけ`Revision = PermissionForbidden`／`MaxProviderCalls = 2`／`ExecutionLimit = maxTasks`を上書きする」——`NewStandard`自体は1行も変更しない。
- **`ExecutionLimit`の実際の意味**：既存`resolveAutonomyContract`（`go/internal/process/interaction_workflow.go:487,544`）は`Contract.ExecutionLimit == workflowPlanInput.MaxTasks`という既存invariantを要求する。したがってbounded profileでは、`ExecuteInteractionPlanApproveAndExecute`が構築する`workflowPlanInput.MaxTasks`も`defaultWorkflowMaxTasks`（=20）ではなく`1`へ明示的に上書きする——既存の`ProviderFixtureMaxCalls`／`fixtureContract`の配線（144〜157行目）と同じ形で、`record.Profile == ProfileBoundedAcceptance`のときだけ`workflowPlanInput.MaxTasks = 1`と`workflowPlanInput.Autonomy = &boundedContract`（`boundedContract := autonomy.NewBoundedAcceptance(employeeIDs, models, 1)`相当）を設定する。この2つの既存overrideパスは互いに独立しており、`ProviderFixtureMaxCalls`はserver-operator専用のtest harness fieldで実HTTP payloadから到達不能のまま維持する。
- **`resolveAutonomyContract`自体は無変更で済む**——同関数は既にoverride Contractに対して次を強制している（`interaction_workflow.go`544〜556行目）。
  - `requested.ExecutionLimit != input.MaxTasks`ならば拒否（543〜545行目）
  - `requested.AllowedEmployeeIDs`／`AllowedModels`が`standard.AllowedEmployeeIDs`／`AllowedModels`と`slices.Equal`でなければ拒否（547〜549行目）——これはReviewer／Assignee由来のEmployee一覧そのものであり、Reviewer・Assigneeがstandardと同一であることも自動的に含む
  - `requested.EffectiveMaxParallelTasks() != standard.EffectiveMaxParallelTasks()`または`EffectiveMaxRevisionCount()`が不一致なら拒否（553〜556行目）

  この3つの既存チェックが、要求6の「Reviewer、Assignee、AllowedEmployeeIDs、AllowedModels、parallel...を含む他の値はstandardと同一にする」をほぼそのまま満たす——bounded profile側で新たに検証を追加する必要はない。
- **`MaxRuntime`についての訂正**：前回案は「既存resolverがMaxRuntimeを比較している」と読める記述をしていたが誤りである。`resolveAutonomyContract`が明示的に比較するのは`ExecutionLimit`／`AllowedEmployeeIDs`／`AllowedModels`／`EffectiveMaxParallelTasks()`／`EffectiveMaxRevisionCount()`の5つだけで、**`MaxProviderCalls`と`MaxRuntime`はこの関数の比較対象に含まれていない**。`MaxProviderCalls`は意図的にbounded profileが上書きする値（既存`ProviderFixtureMaxCalls`と同じ理由でresolverの比較対象から外れている）。`MaxRuntime`はbounded profileが一切上書きしない値であり、`NewBoundedAcceptance`が`NewStandard`の戻り値を`Clone()`してから`Revision`／`MaxProviderCalls`／`ExecutionLimit`の3 fieldだけを上書きする実装である以上、**Clone操作自体によって`MaxRuntime`はstandardの計算値をそのまま引き継ぐ**（resolverによる比較の有無とは無関係に、上書きしないという実装上の事実がこれを保証する）。この実装上の事実を、`NewBoundedAcceptance`が返すContractの`MaxRuntime`が同じ入力で`NewStandard`が返すContractの`MaxRuntime`と一致することを直接assertするunit testで固定する（resolver経由のtestに頼らない、10節のtest matrix参照）。
- **`MaxRevisionCount`は変更しない**（標準のDefault値のまま）。「Revision禁止」は`MaxRevisionCount`を0にすることでは表現しない——`EffectiveMaxRevisionCount()`が0を「未設定・legacy」として`DefaultMaxRevisionCount`へ読み替える既存semanticsと衝突するため。Revision禁止は7節の`Revision`固有チェックだけで表現する。

### 7. Revision禁止の集中境界

- `go/internal/service/reviewed_workflow_run_service.go`には、Request Changes → Revisionへの遷移を行う箇所が構造的に2つ存在することをsourceで確認した：
  1. **sequential `Run`**（200行目付近のメソッド本体、255行目`revised, revisionErr := service.reviser.Execute(ctx, taskID, revisionCommandID)`）。
  2. **parallel `RunParallel`／`runBranch`**（756行目付近の`runBranch`、937行目`revised, revisionErr := service.reviser.Execute(revisionCtx, currentTaskID, revisionCommandID)`、既存の`revisionCount >= maxRevisionCount`ガード＝928〜931行目の直後）。

  この2つに加え、`ResumeRevision`（390行目、Budget Recovery Continuation・ADR-0055）も正式対象とする——bounded profileは`interaction.workflow.recover_revision`をProcess層で無条件拒否する（3節）ため通常は到達しないが、Service境界そのものにも同じ防御を持たせる（下記）。
- **`service.reviser.Execute`の実際の戻り値型は`revision.Result`である**（`ReviewedWorkflowReviser`interface定義、`reviewed_workflow_run_service.go:80〜81`：`Execute(ctx context.Context, sourceTaskID, commandID string) (revision.Result, error)`）。前回案の`execution.Result`は誤りであり訂正する。
- **`autonomy.Contract`全体をmutable Service stateへ保存しない。** 既存の`SetBudgetPolicy`／`SetProgressPolicy`（164〜186行目）は「呼び出し前に一度設定するmutable Service field」という既存パターンだが、Revision許可はこのパターンを踏襲しない——`maxParallelTasks`／`maxRevisionCount`が既に`Run`／`RunParallel`／`ResumeRevision`へ**呼び出しごとの引数**として渡されている既存パターン（`go/internal/process/reviewed_workflow.go:308,310`の`runService.RunParallel(ctx, ..., maxParallelTasks, maxRevisionCount, batchPlanner)`／`runService.ResumeRevision(ctx, ..., maxParallelTasks, maxRevisionCount, batchPlanner)`）へ揃える。`ExecuteReviewedWorkflow`は既に保持している`input.Autonomy`（解決済みContract）から、検証済みの`autonomy.Permission`値1つ（`input.Autonomy.Revision`）だけを取り出し、`Run`／`RunParallel`／`ResumeRevision`の**新しい引数**として渡す。Contract構造体そのものやそのAutonomy全体はServiceの内部stateへは一切保存しない。

  ```go
  func (service *ReviewedWorkflowRunService) Run(
      ctx context.Context, parentCommandID string, maxTasks int, revisionPermission autonomy.Permission,
  ) (ReviewedWorkflowRunResult, error)

  func (service *ReviewedWorkflowRunService) RunParallel(
      ctx context.Context, parentCommandID, correlationID string, maxTasks, maxParallelTasks, maxRevisionCount int,
      revisionPermission autonomy.Permission, batchPlanner ...,
  ) (ReviewedWorkflowRunResult, error)

  func (service *ReviewedWorkflowRunService) ResumeRevision(
      ctx context.Context, parentCommandID, correlationID, revisionTaskID string, maxTasks, maxParallelTasks, maxRevisionCount int,
      revisionPermission autonomy.Permission, batchPlanner ...,
  ) (ReviewedWorkflowRunResult, error)
  ```

  （引数の正確な並び・既存引数との統合順序はPB-3an.2実装時に確定するが、「呼び出しごとの引数として渡す、mutable setterやprocess-local global flagは使わない」という設計はここで確定する。）
- 3つの呼び出し元すべてを、`revision.Result`に一致する単一のpolicy-aware helperへ統一する。

  ```go
  // executeRevisionIfAllowed is the sole caller of service.reviser.Execute
  // in this package. The sequential Run, parallel runBranch, and
  // ResumeRevision code paths all call this instead of
  // service.reviser.Execute directly.
  func (service *ReviewedWorkflowRunService) executeRevisionIfAllowed(
      ctx context.Context, taskID, revisionCommandID string, revisionPermission autonomy.Permission,
  ) (revision.Result, error) {
      if revisionPermission == autonomy.PermissionForbidden {
          return revision.Result{}, ErrRevisionForbiddenByProfile
      }
      return service.reviser.Execute(ctx, taskID, revisionCommandID)
  }
  ```

  `revisionPermission == autonomy.PermissionForbidden`が偽（標準profile、常に`PermissionDelegated`を渡す）の場合は既存どおり`service.reviser.Execute`を呼ぶ——**標準profileの実行パス・戻り値は1バイトも変わらない**。
- **`ResumeRevision`の二重防御**：Process層（`ExecuteInteractionRecoverRevision`、`interaction_recover_revision.go`）が、Session読取直後・`interaction.workflow.recover_revision`という操作そのものをbounded Sessionに対して無条件拒否する（3節）——これが主たる防御線。加えて、`ResumeRevision`自身も受け取った`revisionPermission`が`PermissionForbidden`なら、実際にResumeしようとしているRevision Taskへ到達する前に同じ`executeRevisionIfAllowed`相当の拒否を行う——Process層のガードが将来誤って外れても、Service境界自身が独立してRevision実行を拒否する多層防御とする。
- 停止位置：「Request ChangesのReview／Event／Audit確定後、`reviser.Execute`前に拒否する」。既存コードは既にこの順序（Review Artifact commit → `review.completed` Event発行 → Verdict switch → Revision作成）を守っており、新しいガードをVerdict switchの`RequestChanges`ケース内、`service.reviser.Execute`（＝新helper呼び出し）の直前に置くだけで、この順序を変更せずに満たせる。
- **sequential・parallel・resumeの3経路それぞれにunit testを必須にする**（10節）：`Run`・`runBranch`（`RunParallel`経由）・`ResumeRevision`のそれぞれに対し、`autonomy.PermissionForbidden`を注入したMock Reviserが一度も呼ばれないことをcall counterで直接検証するtestを3本（経路ごとに1本）用意する——grepはCIでの補助的な非回帰チェック（新しい直接呼び出しが紛れ込んでいないかの静的確認）として維持してよいが、正しさそのものの証明はこの3本のunit testが担う。

### 8. Request Changes terminal semantics

既存のTyped FailureEnvelope伝播機構（[ADR-0041](ADR-0041-typed-failure-envelope-propagation.md)、実装済み）をそのまま再利用する。`go/internal/process/reviewed_workflow.go`の`envelope`構築switch（366〜430行目）は、既に`"revision_limit"`／`"no_progress"`という「失敗ではなく意図的な停止」を表す先例を持つ（`REVISION_LIMIT_REACHED`／`NO_PROGRESS_DETECTED`、いずれも`Category`は設定せず`Evidence: {Deliverable: true, TaskState: true, ReviewCanonical: true}`）。同じ既存closed vocabularyの形（Code一つにつきCategoryは関連する下位分類の区別が必要な場合だけ使う、`"budget"`ケースの`Category: "runtime"／"provider_call"`が先例）へ整合させ、新しいcaseを1つ追加する。

```go
case stage == "revision_forbidden":
    // The bounded_acceptance profile's own stop (ADR-0072): the last
    // attempt's execution and Review both committed canonically (the Task
    // completed, the RequestChanges verdict is a real, already-saved
    // Review artifact) -- Go declined to create a Revision because this
    // Session's Autonomy Contract has Revision: PermissionForbidden, not
    // because of a Revision/No-Progress guard limit.
    envelope = failure.New("REVIEWED_WORKFLOW_BOUNDED_STOP", stage)
    envelope.Evidence = &failure.CommittedEvidence{Deliverable: true, TaskState: true, ReviewCanonical: true}
```

`stage`自体（`"revision_forbidden"`という文字列、7節のhelperが返すstage名と一致させる）が、既存の`revision_limit`／`no_progress`／`budget`と同じ位置づけのclosed vocabulary値として整合する。`Category`は追加しない（`budget`ケースのようにCode 1つの下で複数の意味を区別する必要がないため）。

既存の`envelope.Partial = partial; envelope.RecoveryRequired = partial`（431〜432行目）という末尾の一律代入について、前回案は「`revision_forbidden`だけ明示的に上書きする」という曖昧な表現だった。正確には、この末尾2行の直後に次の1行を追加する契約へ訂正する。

```go
envelope.Partial = partial
envelope.RecoveryRequired = partial && stage != "revision_forbidden"
```

これにより`Partial`は既存の`revision_limit`／`no_progress`と同じ値（`partial`、Task・Reviewが実際に committed している限り真になる既存の計算値）を保ちながら、`RecoveryRequired`だけが`revision_forbidden`のときは常に偽になる——「Revision／Recoveryボタンを表示しない」という要求を、既存の一律代入を壊さない最小の式変更で実現する。

- **`interaction.Next()`の実際のfall-through**（`go/internal/interaction/interaction.go:824`〜`855`、`StateWorkflowAttentionRequired`ケース）をsourceで確認した。843行目の`switch workflow.Failure.Code { case "REVISION_LIMIT_REACHED", "NO_PROGRESS_DETECTED": ... case "BUDGET_EXCEEDED": ... }`は2つのcaseしか持たず、`default`は存在しない（＝何もしない）。新しいCode`"REVIEWED_WORKFLOW_BOUNDED_STOP"`をこの2値switchへ**加えない**ことにより、`found`は常に`false`のままとなり、825行目の`next.Kind = NextInspectWorkflow`（このcaseの冒頭で既に設定済み）と830〜833行目の`next.Commands`（workspace／project CommandIDへの参照、これは`workflow.CommandID`／`workflow.WorkflowCommandID`から無条件に設定される、Revision固有ではない）だけが残り、849〜854行目の`next.Operation = "interaction.workflow.recover_revision"`は一切設定されない。したがって`ApprovalRequired`はfalseのまま（`NextAction`のGo zero value）——**`Next()`への変更は不要**で、既存のfall-through automaticaly inspect-onlyになる。UIはこの`next.Kind == NextInspectWorkflow`かつ`next.Operation == ""`（Revision承認を提示しない状態）を、日本語copy「Reviewで修正要求が出たため限定確認を終了しました」で表示する（Code`REVIEWED_WORKFLOW_BOUNDED_STOP`をキーとする新しいcopy 1件を`showError`のcode/stage→copy tableへ追加）。
- Workflow result：`finishDurableCommandWithEnvelope`はerrを返すため、outer Commandは既存の意味で「非success terminal」として記録される（`revision_limit`／`no_progress`と同じ扱い＝技術障害でも成功完了でもない）。

### 9. 並行二重承認の防止（bounded profile限定、4節の実装詳細）

4節に統合済み。標準profileの`ExecuteInteractionPlanApproveAndExecute`は、既存のCommand Ledger claim（`input.CommandID`ベース）以外、一切変更しない——標準profileの既存挙動（潜在的な特性を含め）を変えないという要求1を厳格に守る。

### 10. UI表示

- 依頼作成前の画面（`web/app.js`の`openNewRequestDraft`／composer、`submitDraftRequest`の直前）へ、既定OFFの明示的なtoggle／checkboxを追加する。ONの場合だけ`execution_profile: "bounded_acceptance"`を`interaction.start`のpayloadへ含める（OFFの場合は既存どおりfieldを含めない）。
- toggle選択時とPlan承認画面（`renderPlanApproval`）の両方で、次の固定文言を表示する：「Plan 1回、Task 1件、Review 1回、最大3 calls、clarification／failure／timeout／Request Changesで停止、Revision／Recoveryなし。」
- hidden test modeやfixture overrideとして実装しない——`execution_profile`は他の一般Command payload fieldと同じ経路（`commandcontract`のJSON Contract v1検証）で検証される、正式かつHTTPから到達可能なfield。
- credentialはこの機能追加によって一切browser／HTTP payload／storageへ渡らない。

### 11. Compatibility／security

- Keychain、credential resolution、Provider endpoint、model routingへの変更は一切ない。
- secret export、環境変数への転記、HTTP credential field追加は行わない。
- 本profileはadditiveかつoptionalであり、`Profile`フィールドを持たない（＝空文字列の）既存Sessionと標準呼び出し元は、この変更の影響を一切受けない。
- 本ADRおよびそのPB-3an.2実装は、PB-3ag／PB-3jで観測されたPacked binary Keychain ACL問題やProvider failureの根本原因を修正するものではなく、実Providerでの成功を確認するものでもない。

## Rejected Alternatives

- **`MaxRevisionCount = 0`をRevision禁止として使う**：既存の`EffectiveMaxRevisionCount()`が0を「未設定・legacy」として`DefaultMaxRevisionCount`へ読み替えるため、実際には禁止にならない。
- **BudgetGuard（`MaxProviderCalls`）だけでRevision作成を止める**：Revision作成自体がProvider呼び出しを1件も伴わないため、原理的に不可能。
- **Provider API keyをoperator CLIへexportし、専用processとして隔離実行する**：Work自身の依頼で明示的に禁止された方向性。既存credential resolutionの閉じたsource（[ADR-0066](ADR-0066-headless-credential-resolution.md)）と衝突する。
- **`interaction.plan.approve_and_execute`のペイロードへ`execution_profile`を含めさせ承認のたびに指定させる**：Profileは新規依頼作成時にだけ選び、Session自体へ拘束する（要求1）。
- **`existing budgetTracker`をVault-backedなdurable counterへ拡張する**：ADR-0054のBudgetGuard v1が明示的にprocess-local・1 execution scopeとして設計されている。Plan生成側だけに新しいdurable reservation（2〜3節）を追加し、Reviewed Workflow側は既存のまま再利用する非対称な設計の方が変更範囲を小さく保てる。
- **`interaction.New()`の戻り値へ外部からexported fieldを直接代入する**（前回案、本Checkpointで撤回）：`Record`がconstructor以外の経路でも部分的に構築可能になり、将来の呼び出し元が不変性チェックを経ずにfieldを書き換えられる余地を残す。`NewWithProfile`という検証済みconstructorへ置き換えた。
- **架空のparent Command IDを`DeriveChildCommandID`へ渡してreservation IDを作る**（前回々回案、本Checkpointより前に撤回済み）：`DeriveChildCommandID(parentCommandID, aggregateID)`は「実在するparentの子」という意味論を持つ既存関数であり、意味的に無関係な固定文字列を渡すのは誤用になる。
- **`parts ...any`を受ける汎用primitiveでreservation IDを作る**（前回案、本Checkpointで撤回）：可変長引数は要素の型・順序が呼び出し側の裁量に委ねられ、encodingが固定されない（素朴な連結では`["ab","c"]`と`["a","bc"]`が衝突し得る）。closedな型付き入力struct（`ApprovalReservationID`）＋versioned domain separator＋固定fieldの`json.Marshal`という専用`DeriveApprovalReservationID`primitiveへ改めた（4節）。

## 変更ファイル（PB-3an.2で実装済み、PB-3an.2bでfocused correction）

| ファイル | 変更内容 |
|---|---|
| `go/internal/autonomy/contract.go` | `Validate()`のRevision判定緩和、`NewBoundedAcceptance`追加 |
| `go/internal/interaction/interaction.go` | `Profile`型・定数、`Record.Profile`、`TurnPlanGenerationReserved`／`ReservedChildCommandID`、`NewWithProfile`、`RecordPlanGenerationReservation`、`ValidateTransition`のProfile不変性、`Validate()`の新Turn caseとcross-kind validation（7節）。**`Next()`のprofile-aware分岐（3節で撤回・再設計）：`StatePlanGenerationApprovalRequired`／`StateClarificationRequired`のbounded分岐を追加** |
| `go/internal/process/interaction.go` | `ExecuteInteractionStart`が`NewWithProfile`を呼び、outer claim requestへ`Profile`fieldを追加（2節：interaction.start claim digest）。`executeInteractionPlanGenerationCommand`へ2〜3節のreservationチェックを追加。`ExecuteInteractionAnswer`の入口でbounded拒否。**standalone `interaction.plan.apply`ハンドラ（777行目、前回`interaction_workflow.go`と誤記していたファイル）の入口でbounded拒否** |
| `go/internal/process/interaction_workflow.go` | standalone `interaction.workflow.execute`ハンドラの入口でbounded拒否 |
| `go/internal/process/interaction_recover_revision.go` | `ExecuteInteractionRecoverRevision`の入口でbounded拒否（3節・7節のProcess層防御） |
| `go/internal/process/interaction_approve_and_execute.go` | 3節（1 Task preflight統合）、4節（approval reservation ID生成・claim・consumed finish・crash分類）、6節（`MaxTasks=1`＋bounded Contract override配線）を追加 |
| `go/internal/commandledger/ledger.go` | `ApprovalReservationID`型と`DeriveApprovalReservationID`primitiveの新設（4節）とそのunit test |
| `go/internal/autonomy/contract.go`（再掲） | `Permission`型は既存のまま、`Run`／`RunParallel`／`ResumeRevision`へ渡す`revisionPermission autonomy.Permission`はProcess層（`ExecuteReviewedWorkflow`）が`input.Autonomy.Revision`から取り出す（7節）。Contract型自体の追加変更なし |
| `go/internal/service/reviewed_workflow_run_service.go` | `Run`／`RunParallel`／`ResumeRevision`の3シグネチャへ`revisionPermission autonomy.Permission`引数を追加（mutable setterは追加しない）。単一の`executeRevisionIfAllowed`（戻り値`revision.Result`）ヘルパーへ、sequential `Run`・parallel `runBranch`・`ResumeRevision`の3箇所の`reviser.Execute`呼び出しを統一（7節） |
| `go/internal/process/reviewed_workflow.go` | `ExecuteReviewedWorkflow`が`input.Autonomy.Revision`を`RunParallel`／`ResumeRevision`へ渡すよう配線（7節）。8節の`"revision_forbidden"` envelope caseと`RecoveryRequired`式の訂正を追加 |
| `go/internal/commandcontract/payload.go` | `interaction.start`へoptional `execution_profile`検証を追加 |
| `go/internal/httpapi/executor.go` | `execution_profile`をpayloadから`InteractionStartInput`へ橋渡し |
| `go/internal/httpapi/web/app.js` | composer画面のtoggle、Plan承認画面のread-only表示、固定文言、`NextInspectWorkflow`／空`Operation`（Plan生成stageのbounded停止、3節）と`REVIEWED_WORKFLOW_BOUNDED_STOP`（Request Changes停止、8節）向けerror copy |

## Test matrix（PB-3an.2で実装済み、PB-3an.2bで追加・修正）

既存のMock E2E／Browser Gate matrix（`TestReviewedWorkflowTemporaryVaultRequestChangesRevisionReReviewAndReplay`系、`TestMobileInteractionHTTPFlow`系、Public Beta Browser Acceptance Gate）は維持し、そこへ次を追加する。grepによる静的確認は補助チェックとして残してよいが、下記のbehavior testが正しさの根拠そのものである。

- **standard既存JSON／digest compatibility**：standard Sessionの既存golden RequestDigest値・既存JSON serializationが本変更の前後でbyte-for-byte一致すること。
- **`Record.Validate()`のclosed-value guard（domain test）**：`Profile: ProfileStandard`および`Profile: ProfileBoundedAcceptance`を持つ、他はすべて有効なRecordがいずれも`Validate() == nil`（accept）であること。`Profile`に上記2値以外の任意の文字列（空でも`"bounded_acceptance"`でもない値、例：`"bounded"`という似た誤入力や将来のprofile名を想定した未知の値）を設定したRecordが`Validate() != nil`（reject）であること。
- **persisted JSON／Store loadでのunknown Profile拒否（境界test）**：`go/internal/adapter/vault/interaction_store.go`の`InteractionStore.Get`（71〜78行目が呼ぶ`store.read`、167行目のdecode直後の`record.Validate()`チェック）に対し、`profile`フィールドへ不正な値（未知文字列）を直接書き込んだhand-crafted JSON fixtureを読み込ませ、`ErrInvalidSession`で拒否されること（正常な既存Recordの読込には影響しないこと）。同様に、`encodeInteractionRecord`（179行目）が、program内でunknown Profileを持つに至った（本来あり得ないが）Recordの書込を`Validate()`の時点で拒否すること。
- **unknown ProfileでのeffectへのUnreachability（fail-closedの端から端までの確認）**：`Record.Validate()`が拒否する以上、`record.Validate() != nil`を前提条件に含む既存のあらゆる`Record*`メソッド（`RecordPlan`／`RecordAnswers`／`RecordApplied`／`RecordWorkflow`等、いずれも各メソッド冒頭で`record.Validate() != nil`を拒否条件に含む）は、unknown Profileを持つRecordに対して即座にエラーを返すことを確認する。同じ理由で`Next()`（766行目、冒頭で`record.Validate() != nil`を返す）もエラーを返すため、unknown ProfileのSessionに対してPlan生成・Apply・Workflow実行のいずれのnext-actionも提示されず、Process層（`executeInteractionPlanGenerationCommand`等）がSessionを読み込んだ時点で同じ理由により`err != nil`となり、Provider／Project／Task effectのいずれにも到達しないことを、少なくとも1つの代表的な経路（例：`ExecuteInteractionPlanApproveAndExecute`がunknown Profileを持つSessionを読み込んだ場合）でE2E的に確認する。
- **interaction.start claim digest**（2節）：(a) `Profile`省略時のCommand request digestが本変更前後でbyte-for-byte一致すること。(b) 同一`CommandID`・同一request・同一modelで、一度standard（`Profile`省略）でclaimした後、bounded（`Profile: "bounded_acceptance"`）で再送すると、既存のrequest digest不一致検出により「異なるrequest」としてreplayではなくconflict拒否されること。
- **v1→v2→v3 Version／CAS**：bounded Sessionで`interaction.start`成功後にVersion=2（reservation Turn）、Provider成功後にVersion=3（Plan Turn）となること。Provider失敗後はVersionが2のまま留まること。
- **daemon crash後のPlan再生成拒否**：v2 commit後、同一Sessionへの新しいCommand ID（異なるouter Command由来）による`interaction.plan.generate`／`interaction.answer`経由の再試行がProvider呼び出しなしで拒否されること。
- **profile-aware `Next()`**（3節）：(a) bounded Sessionでreservation済み・`TurnPlanGenerated`未到達（失敗／timeout想定）のとき、`Next()`が`NextInspectWorkflow`／空`Operation`を返しanswer／generate／regenerateを一切提示しないこと。(b) bounded Sessionが`StateClarificationRequired`に到達したとき、reservationの有無を問わず同様にinspect-onlyであること。(c) standard Sessionでは`Next()`の出力が本変更前後で一致すること（非回帰）。(d) HTTP projection（Next()をそのまま返すエンドポイント）とbrowser UI（`renderWorkflowApproval`等の既存`Operation`空値分岐）が同じinspect-only状態を表示すること。
- **全standalone経路のeffect-before拒否**：bounded Sessionに対する standalone `interaction.plan.apply`（`interaction.go`）／`interaction.workflow.execute`（`interaction_workflow.go`）／`interaction.workflow.recover_revision`（`interaction_recover_revision.go`）が、Project／Task／Provider効果より前に拒否され、Mock Providerが一度も呼ばれないこと。
- **outer replay**：`interaction.plan.approve_and_execute`の同一`CommandID`によるreplayが、reservationに触れず既存のterminal outer resultをそのまま返すこと。
- **異なるCommand IDの並行承認**：同一`{SessionID, ExpectedVersion, PlanDigest, Profile}`に対し異なる`CommandID`を持つ2つの`approve_and_execute`呼び出しのうち、片方だけが成功しProject／Taskが1件しか作られないこと。
- **approval reservationのrunning／terminal／crash各状態**：reservation claimが`running`中の2件目、既に`consumed`後の2件目、いずれもtyped non-successで拒否され、outer Command自身がterminal（非running）でfinishされること。
- **approval reservation ID primitive**（4節）：`ApprovalReservationID`の`{SessionID, ExpectedVersion, PlanDigest, Profile}`が1つでも異なれば別のIDになること（Version差・Profile差・PlanDigest差それぞれ）。境界曖昧性の非衝突（例：`PlanDigest="AB", Profile="C"`と`PlanDigest="A", Profile="BC"`が異なるIDになること）。
- **sequential・parallel・resumeの3経路のRevision 0**：`autonomy.PermissionForbidden`を注入したMock ReviserのExecute call countが、sequential `Run`・parallel `runBranch`（`RunParallel`経由）・`ResumeRevision`の3経路すべてで0であること。
- **Request Changes後のReview／Audit／Ledger／inspect-only UI**：Review artifactが保存され、outer CommandがLedgerへ`REVIEWED_WORKFLOW_BOUNDED_STOP`／`RecoveryRequired=false`として記録され、`Next()`が`Operation=""`（recover_revisionを提示しない）のinspect-onlyを返すこと。
- **standard profile非回帰**：本変更前後で、standard profileの`autonomy.NewStandard`出力、`resolveAutonomyContract`の挙動、`ExecuteInteractionPlanApproveAndExecute`の既存test（Mock E2E）が引き続きすべてPASSすること。
- **credential／secretがHTTP／browserへ追加されないこと**：新しいpayload field（`execution_profile`）にcredential値が一切含まれないこと、既存のcredential関連field allow-listが変更されていないこと。

**PB-3an.2bで追加したtest（Implementation Findings 3〜7への対応）**

- **Canonical Plan reservation invariant**：先行reservationのないbounded `TurnPlanGenerated`のdomain拒否、`ReservedChildCommandID`不一致の拒否、Planより後のreservationの拒否、Store decode／write時の同invariant拒否、standard `RecordPlan`の非回帰。
- **HTTP／browser双方のRecoveryRequired直接assert**：`mapCommandError`のunit test（Envelope優先／legacy fallback／standard recoverable failureがtrueのまま維持されること）、real HTTP Handlerを使った同期初期応答とstatus polling応答の両方でRecoveryRequired=falseを直接確認するE2E test、real browserでの初期表示・reload後表示の両方でRecovery actionが出ないことを確認するPlaywright test。
- **真の並行承認test**：2 goroutine＋barrier channelによる実際の競合、`go test -race`で20回連続PASS。winner1／loser1／Project1／Task1／Task Provider call1／Review Provider call1／reservation record1／outer Command両方terminal／reservation terminalを直接assert。
- **`DeriveApprovalReservationID`のProfile閉包**：`bounded_acceptance`のみ受理、空／`standard`／任意文字列／末尾空白付きはすべて拒否。
- **標準digest互換／conflict**：standard `interaction.start`のCommand claim digestが変更前後でbyte-for-byte一致、同一Command IDでprofileだけ変えるとconflict。
- **timeout変種**：実際のclient-side timeoutでもreservationが維持され再attemptが0であること（503とは別の実装経路として直接確認）。
- **Request Changes後の直接evidence**：Review artifact本文（Request Changes verdict）、Audit Log entry、`review.completed` Event発行回数（1）を直接assert。
- **Browser E2E**：`tests/browser/bounded-acceptance.spec.mjs`（新規、fixtureに`bounded_acceptance_request_changes`／`bounded_acceptance_approve`の2 scenarioを追加）。

**PB-3an.2dで強化したtest（Implementation Findings 8への対応、production codeは無変更）**

- **並行承認testの決定性強化**：PB-3an.2bの`TestBoundedApproveAndExecuteConcurrentApprovalRaceOnlyOneWins`は2 goroutineをbarrier channelで同時解放していたが、それだけでは「loserがwinnerとreservationを巡って実際に衝突した」ことの決定的な証拠にならず、たまたま正しい終端状態になっただけの可能性を排除できていなかった。test専用のblocking fake（`raceApproveAndExecuteMockServer`）をMock Providerへ追加し、winnerがreservationを`consumed`にした直後・Task execution Provider callへ到達した時点で応答をpauseさせ、winnerがまだ返っていないことを確認しながらloserが独立にterminal状態へ到達するのを待ってから初めてwinnerを解放する構造へ書き換えた。sleep・scheduler運任せ・timeoutを根拠にした判定は一切使わず、安全弁のtimeoutのみ許容している。winner1／loser1／winner outer Ledger=succeeded／loser outer Ledger=failed／loser failure code=`INTERACTION_APPROVE_AND_EXECUTE_FAILED`・stage=`approval_reservation`／reservation Ledger=succeeded・`consumed=true`／Project1／Task1／canonical Review1（`Review.Artifact.CanonicalCommitted`直接assert）／Revision Task・Command・intent/artifact全て0（`RevisionCommandID==""`かつ`Revision==nil`の直接assert）／Task Provider call1／Review Provider call1／retry・fallback・追加Review0を、すべて直接assertする形へ強化した。`go test -race`で20回連続PASSを確認済み。
- **Browser E2Eのassertion強化**：`tests/browser/bounded-acceptance.spec.mjs`のRequest Changes停止scenarioで、Recovery系のquick reply button（`renderRevisionRecovery`が実際に使う正確な文字列である「この指摘を踏まえて修正を続ける」／「必要な部分だけ続ける」）が停止直後とreload後の両方で存在しないことを、以前の緩い正規表現（`/追加の指示/`・`/修正を依頼/`）ではなく実際の製品文字列で直接assertする形へ強化した。同時に、composerの`data-mode="idle"`・readonly状態、`#details-panel`のTask・Review evidence（Request Changes verdictの永続表示）を停止直後・reload後の両方で直接assertするヘルパーを追加した。Provider callについては、fixtureが3件のresponseを持つことに頼るのではなく、実際に観測された`environment.provider.calls`配列を直接検査し、structured/unstructuredの順序（Plan=structured, Task=unstructured, Review=structured）とfixture名の順序をexact matchでassertする形へ強化した。standard profileについては、UIの文言ではなく実際にwireへ流れた`interaction.start`のrequest body（`page.on("request")`によるnetwork capture）を直接検査し、`execution_profile`キー自体が存在しないことを新規testで確認した。production側のtest-only bypassやhidden flagは一切追加していない。

## Implementation Findings Resolved

targeted testが、ADR本文の設計だけでは見えていなかった実装上のギャップ・実バグを、PB-3an.2（初回実装）とPB-3an.2b（Codex focused reviewへの対応）の2回にわたって検出し、検出と同じCheckpoint内で修正した。技術的な指摘はすべてここに集約し、下の「Open Questions」には実装blockerを残さない。

**PB-3an.2（初回実装時に検出）**

1. **`RecoveryRequired`の二重計算**。8節は`reviewedWorkflowOuterEnvelope`（`go/internal/process/reviewed_workflow.go`）の末尾代入だけを`partial && stage != "revision_forbidden"`へ訂正する設計だったが、Interaction Workflow層がこのEnvelopeを再度ラップする別関数`workflowFailure`（`go/internal/process/interaction_workflow.go`）が独立に`envelope.RecoveryRequired = partial`を無条件代入しており、正しく計算した`false`を`true`へ上書きしてしまうことをtestで検出した。同じ例外条件を`workflowFailure`にも追加して解消した。
2. **`RevisionCommandID`の幽霊参照**。forbidden判定より前に`revisionCommandID`の導出と`branchResults[lastIndex].RevisionCommandID`への代入が行われており、claimもexecuteもされていないCommand IDがTask evidenceへ記録されてしまうことをtestで検出した。既存の`revision_limit`ガードと同じ位置（Command ID導出より前）へforbidden判定を移動し解消した（sequential `Run`・parallel `runBranch`の両方）。

**PB-3an.2b（Codex focused review PB-3an.2aのP1 4件・P2 2件・P3 1件への対応）**

3. **Canonical Plan reservation invariantの欠落（P1）**。`RecordPlan`／`Record.Validate()`は、bounded_acceptance Sessionの`TurnPlanGenerated`が実際に先行`TurnPlanGenerationReserved`を持つかを検証しておらず、`RecordPlan`単体の呼び出し規律だけに依存していた。`Validate()`へ「bounded Sessionの`TurnPlanGenerated`は、直前までに存在した唯一の予約Turnと同じ`ReservedChildCommandID`を持たなければならない」という相互検証を追加し、`RecordPlan`自体にも先行予約の必須化を追加した（ドメイン層自体が invariant を強制するようになった）。自テストのfixture（`writeBoundedApprovalRequiredSession`等）がこの invariant に違反していたため、v1（Session作成）→v2（予約）→v3（Plan）の正式順序へ修正した。
4. **`RecoveryRequired`のHTTP top-level投影（P1）**。`go/internal/httpapi/handler.go`の`mapCommandError`が`RecoveryRequired: recorded.Partial`を無条件に使っており、Envelope自体が正しく`false`を計算していても、HTTPレスポンスの最上位`error.recovery_required`は`true`のままになっていた（Envelopeの値と最上位fieldの値が食い違う二重管理バグ）。`recoveryRequiredFor(recorded)`ヘルパーを新設し、Envelopeが存在する場合はその値を正本とし、legacy（Envelopeなし）recordだけPartialへfallbackする契約へ統一した。**ブラウザ側にも独立した同種のバグがあった**：`web/app.js`のpolling／reload復元ロジックが`recovery_required: record.state === "partial_failure"`という状態文字列だけからの再生成を行っており、Envelopeの`details.recovery_required`を無視していた。共通ヘルパー`recoveryRequiredFromRecord(record)`へ統一し、Envelopeがあればそれを正本、なければ既存のstate文字列fallackを維持する契約へ修正した。
5. **並行承認testが実際には逐次だった（P1）**。前回のtestは2回の`ExecuteInteractionPlanApproveAndExecute`呼び出しを順番に（1回目が完全終了してから2回目を開始）実行しており、「並行」ではなく「終端済みreservationへの後続拒否」を検証していた。前者を`TestBoundedApproveAndExecuteTerminalReservationRejectsSecondOuterCommand`へ正確に改名し、実際に2つのgoroutineをbarrier channelで同時解放する`TestBoundedApproveAndExecuteConcurrentApprovalRaceOnlyOneWins`を新設した（`go test -race`で20回連続PASSを確認）。
6. **`DeriveApprovalReservationID`のProfile検証が緩すぎた（P1）**。空文字列や任意の文字列（例："A"）を有効なProfileとして受理していた。`bounded_acceptance`という単一のリテラル文字列だけを受理する契約へ修正し、空／`"standard"`／任意文字列／末尾空白付きの`"bounded_acceptance "`のいずれも拒否することを確認した。既存test（`TestDeriveApprovalReservationIDDiffersOnEachField`の`Profile=""`ケース、naive-concatenation collision testの`Profile=""`/`"A"`ケース）を、この契約と矛盾しない形へ書き換えた。
7. **不足していた境界test（P2/P3）**。standard `interaction.start` Command claim digestの既存互換（byte-for-byte一致）、同一Command IDでstandard→boundedへ変えるとrequest conflictになること、Store decode／encodeでのunknown Profile拒否、HTTP executorから`InteractionStartInput.Profile`への伝播、Provider timeout相当（実際のclient-side timeout、503とは異なる形）でも予約が維持され再attemptが0であること、Request Changes後のReview artifact本文／Audit Log／`review.completed` Eventの直接assertを追加した。

**PB-3an.2d（Codex focused review PB-3an.2cのP1 2件への対応、production codeは無変更）**

8. **test evidenceの厳密性不足（P1 2件）**。PB-3an.2bで実装した並行承認testおよびBrowser E2E testは、実装そのものの欠陥ではなく、実装が実際に保証していることを証明する厳密さが不足していた。並行承認testは2 goroutineを同時解放してはいたが、winnerとloserが実際に時間的に重なって競合したことを確認する仕組みがなく、たまたま正しい結果になっただけの可能性を排除できていなかった。Browser E2Eは、Recovery系button非存在の確認に緩い正規表現を使い、Provider呼び出し回数・順序をfixtureの scripted response数に依存して間接的にしか確認しておらず、standard profileでの`execution_profile`欠落もUI文言経由の間接確認に留まっていた。上の「Test matrix」節「PB-3an.2dで強化したtest」に記載した通り、test専用のblocking fakeによる決定的な並行性証明、実際の製品文字列でのassertion、`environment.provider.calls`の直接検査、network capture経由の直接検査へ、それぞれ書き換えることで解消した。**この対応でproduction defectは1件も新たに見つかっていない**——見つかっていた場合は本Checkpointの制約（production codeを変更しない）に従い、その場で停止しWorkへ報告する契約だったが、そのケースには該当しなかった。

これらすべての指摘はPB-3an.2b（Implementation Findings 1〜7）およびPB-3an.2d（Implementation Findings 8）のCheckpoint内で修正・testが緑になることまで確認済みであり、新たなOpen Questionsとしては残していない。

本ADRおよび添付のfile map／test matrixは、PB-3ag／PB-3jで観測されたProvider failure根本原因を修正したとも、実Providerでの成功を確認したとも、Public Beta GOに近づいたとも主張しない。現行のsigned candidate（Manual macOS Signed Release Procedureに基づく既存candidate、Acceptance evidence）に対する変更・再利用可否の判断は本Checkpointの対象外であり、一切行っていない。

## Open Questions

なし。技術的な指摘・実装ギャップはすべて上の「Implementation Findings Resolved」に集約し、その場で解決済みである。PB-3an.2eのCodex implementation reviewがGOを出し、本Checkpoint（PB-3an.2f）でStatusを`Accepted`へ変更したため、GO待ちという運用上の理由も含め、未解決の疑問点は残っていない。

## Consequences

- 標準profileの挙動・serialized出力・test golden値は変更していない（既存test全件・新規追加testの両方でPASSを確認済み、`-race`含む）。
- bounded_acceptance profileは、Session作成時に明示的にoptしない限り一切有効化されない。
- Canonical Plan reservation invariantがドメイン層自体（`Record.Validate()`／`RecordPlan`）で強制されるようになり、Provider呼び出しの安全な上限が、Session全体を通じて（process再起動を含め、標準経路以外のstandalone入口を含め、実際のclient timeoutを含め）durableに保証されることが、targeted testと実際のgoroutine競合testの両方で確認済みである。
- `RecoveryRequired`の投影規則（Envelope優先、legacyのみPartialへfallback）がGo側HTTP応答とbrowser側polling／reload復元の両方で統一され、real HTTPラウンドトリップtestとreal browser E2E testの両方でsynchronous初期応答・status polling応答・reload後表示の3点が直接assert・確認済みである。
- Revision作成を止める新しいruntime enforcementが、既存のtyped `Permission`列挙の未使用だった`Revision`fieldへ初めて実際の意味を与えた。
- 一般browser UI（既定OFFのtoggle、Plan承認画面のread-only表示、bounded stop専用copy）を、実際のdaemon binary・temporary Vault・fixture-drivenのMock Providerを使った3本のPlaywright scenarioで、Request Changes停止経路・Approve完了経路・standard profileでの`execution_profile`欠落の3点についてEnd-to-Endで確認した。Request Changes停止経路は、PB-3an.2dで実際の製品button文字列・composerのdata-mode／readonly状態・Review evidenceの永続表示・`environment.provider.calls`の直接検査（回数・順序）へ強化済みである（PB-3an.2b時点ではより緩い正規表現とfixture形状への依存のみだった）。
- 本ADRの実装（PB-3an.2／PB-3an.2b／PB-3an.2d）はGoファイル・test・Web UI・Browser specの追加・修正まで完了しており、PB-3an.2eのCodex implementation reviewがGO（31ファイルcommit-ready、ADR Accepted昇格可能）を出したことを受け、本Checkpoint（PB-3an.2f）でStatusを`Accepted`へ変更し、当該31ファイルをcommitした。`make check-ui-full`／`make public-beta-smoke`／`make v1-release-gate`のfull Automated Gates、push、tag再作成、candidate再生成、Human Provider／Upgrade Acceptanceは、この新HEADに対して別Checkpointで実施する（本Checkpointでは行っていない）。現行のsigned candidateおよびAcceptance evidenceは旧HEAD由来のものであり、新HEADのRelease evidenceとして再利用しない。
