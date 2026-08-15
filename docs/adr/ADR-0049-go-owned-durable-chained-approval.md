# ADR-0049: Go-owned Durable Chained Approval for Plan Apply and Reviewed Workflow Execution

## Status

Accepted

## Context

Public Beta通常フローは、CEOが依頼を送ってから完了するまでに次の承認を要求していました。

```
依頼送信
→ 「Plan生成してよいですか」（plan_generation_approval_required）
→ Plan生成
→ 質問があれば回答 → 再び「Plan生成してよいですか」
→ 「このPlanを適用しますか」（interaction.plan.apply）
→ Project／Task作成
→ 「Workflowを実行しますか」（interaction.workflow.execute）
→ Task実行／Review／必要なRevision
→ 完了
```

CEOが実際に判断すべき瞬間は「依頼を送る」「Planを見て進めると決める」の2つですが、上記フローはそれぞれの間に機械的な追加承認を挟んでいました。特に

- `interaction.plan.generate`（初回・clarification回答後）は、CEOが既に承認した依頼／回答からGoが自動的に導出できる作業であり、追加の意思決定を伴いません。
- `interaction.workflow.execute`のReviewer・Autonomy Contract・Task上限は、既存実装でもCEOが実際にカスタマイズしたことがなく（Web UIは常に`max_tasks: 20`を固定送信し、Reviewerは`PlanInteractionWorkflow`のプレビューが機械的に解決した値をそのまま送り返すだけでした）、事実上Go側が既に決定していた値をCEOに承認だけさせる形骸化した手続きでした。

## Decision

CEOの通常操作を2回の明示承認へ縮約します。承認範囲そのものをGoのInteraction State Machineが表現し、UIが承認ステップを勝手に省略するのではありません。

### 1. Request Submit Approval Scope（操作A）

`interaction.start`（CEOの依頼送信）の承認範囲を、意味理解・clarification検出・Plan生成までに限定して拡張しました。`ExecuteInteractionStart`はSession作成が成功した直後、同一の durable Command実行の中で、決定的に導出した子Command ID（`commandledger.DeriveChildCommandID(outerCommandID, "interaction.plan.generate:"+sessionID)`）を使い`interaction.plan.generate`の核となる処理（`executeInteractionPlanGenerationCommand`、標準命令と共有）へ直接進みます。これはUIが2つのCommandを連続送信しているのではなく、Go側のInteraction semanticsとして「依頼送信は既にPlan生成まで承認済みである」ことを表現したものです。

同様に、`interaction.answer`（CEOのclarification回答）も回答commit直後、同じ`interaction.plan.generate`の核へ、回答由来の新しいSession versionを引数として直接進みます。これにより「clarification回答 → 再度plan_generation_approval_requiredで停止 → 別の承認」という従来の往復は、通常経路では発生しません。

`StatePlanGenerationApprovalRequired`という状態・`NextApprovePlanGeneration`というNext種別、および対応する既存の標準`interaction.plan.generate`コマンドは削除していません。同期実行内でのchain継続が完了する前にdaemonがcrashした場合や、稀な観測タイミング（連続実行の途中を別クライアントが読む場合）に限り、Sessionはこの状態に留まり得ます。その場合、既存の標準`interaction.plan.generate`コマンドがそのまま人間による明示的な再試行手段として機能します（新しい復旧機構は追加していません）。

### 2. Single Execution Approval Scope（操作B、最重要）

新設した outer durable command `interaction.plan.approve_and_execute` が、CEOの「この内容で進める」という単一の明示承認を、次の2つの子Commandへの承認として扱います。

```
interaction.plan.approve_and_execute
  |
  +-- child: ceo_plan.apply（deterministic child ID, workspace scope）
  |
  +-- child: workflow.reviewed.execute（deterministic child ID, project scope）
```

`Record.Next()`は`StatePlanApprovalRequired`のとき、`operation`フィールドを`"interaction.plan.apply"`から`"interaction.plan.approve_and_execute"`へ変更しました。既存clientは`next.operation`を動的に読んでCommandを送る設計（`interaction-next`のread modelパターン）を既に採用しているため、UI側の実装を変更せずとも、新しいCommand名へ自然に遷移します。**これはUIがボタンを自動クリックする実装ではなく、Goのcanonical Next contract自体が承認範囲を表現しています。**

### 3. orchestration owner

Plan apply完了後にReviewed Workflow実行を開始する責務は、`ExecuteInteractionPlanApproveAndExecute`（Go Process層）が単独で所有します。ブラウザ／clientは`interaction.plan.approve_and_execute`という1つのouter Commandを送るだけで、`ceo_plan.apply`・`workflow.reviewed.execute`のいずれのchild Commandも送信しません（`commandcontract.ValidatePayload`はこのCommandのpayloadを`interaction.plan.apply`と同一shapeへ厳格化しており、`reviewer_id`・`max_tasks`・`workflow_plan_digest`などのfieldを一切受け付けません）。Reviewer・Autonomy Contract・Task上限（`defaultWorkflowMaxTasks = 20`、既存Web UIが常に固定送信していた値をGo側の既定値として明文化）はすべてGoが内部で決定します。

### 4. child Command Ledgerを維持

`ceo_plan.apply`と`workflow.reviewed.execute`は、それぞれ独立したLedger claim／replay／finishを持つ既存Commandをそのまま再利用しています。`ExecuteInteractionPlanApproveAndExecute`はLedgerを無視した単なる関数呼び出しへ縮退させず、各childへ実CommandIDを渡して既存の`claimWorkspaceCommand`／`claimProjectCommand`経路をそのまま通します。

- `ceo_plan.apply`: workspace scope（Project directoryが未作成のため、既存のscope規則をそのまま踏襲）。
- `workflow.reviewed.execute`: project scope（既存の`ExecuteReviewedWorkflow`のscope規則をそのまま踏襲）。

`interaction.start`→`interaction.plan.generate`のchain継続についても同様に、`executeInteractionPlanGenerationCommand`は毎回、渡されたCommandID（標準経路ではCEO承認済みID、chain経路では決定的導出ID）で独立したLedger claimを行います。

### 5. deterministic child IDs

すべてのchild Command IDは`commandledger.DeriveChildCommandID(parentCommandID, discriminator)`（既存のADR-0021由来のSHA-256ベース関数、無変更）から決定的に導出します。

- `ceo_plan.apply`: `DeriveChildCommandID(outerCommandID, "ceo_plan.apply:"+sessionID)`
- `workflow.reviewed.execute`: `DeriveChildCommandID(outerCommandID, "workflow.reviewed.execute:"+sessionID)`
- `interaction.plan.generate`（`interaction.start`chain）: `DeriveChildCommandID(startCommandID, "interaction.plan.generate:"+sessionID)`
- `interaction.plan.generate`（`interaction.answer`chain）: `DeriveChildCommandID(answerCommandID, "interaction.plan.generate:"+sessionID)`

`time.Now()`、乱数、client側で生成された2つ目のCommand IDには一切依存しません。同一のouter Command IDとSession IDから、いつでも同じchild IDを機械的に再構成できます（`TestInteractionPlanApproveAndExecuteSucceedsAndRecordsBothChildLedgers`が、実際にclaimされたIDと再計算したIDの一致を固定しています）。

### 6. durable approval evidence

既存`policy.ApprovalEvidence{Granted, Source, Reference}`をそのまま再利用しました。新しい永続型は追加していません。CEOの「この内容で進める」という承認事実そのものは、**outer Command Ledgerレコード自体**（`interaction.plan.approve_and_execute`のrunning／terminalレコード）が既にcanonical・durableな証拠です。Reviewed Workflow child実行時のTask-levelポリシー証跡（`ApprovalReference`）には、`"interaction.plan.approve_and_execute:" + outerCommandID`という、どのouter承認に基づく実行かを追跡できる文字列を設定します。

加えて、`interaction.Turn`へ追加した`PreAuthorizedWorkflowCommandID`フィールド（`TurnPlanApplied`にのみ設定、空文字列は「標準`interaction.plan.apply`経由」を意味）が、Session自身の観点からも「このPlan applyはどのouter承認の下で行われ、続くWorkflow実行は既に承認済みか」を機械的に判定可能にします。この2つ（Ledgerレコードの存在、Session Turnのマーカー）の組み合わせが、本ADRにおける「durable approval evidence」の実体です。新しいストレージ層は追加していません。

### 7. crash semantics（section 11 A〜F）

`Record.PendingWorkflowPreAuthorization()`は、直近のTurnが`TurnPlanApplied`かつ空でない`PreAuthorizedWorkflowCommandID`を持つ場合にのみ、そのCommand IDを返します。これにより次を機械的に区別できます。

| ケース | Ledger／Session状態 | 判定方法 |
|---|---|---|
| A. 承認済み、Plan apply未着手 | outer running、`ceo_plan.apply` child不在 | outerをinspect→running、決定的child IDをinspect→NotFound |
| B. Plan apply成功、Workflow未着手 | outer running（またはcrash後もrunningのまま）、`ceo_plan.apply` succeeded、`workflow.reviewed.execute` child不在 | Session側`PendingWorkflowPreAuthorization()`が非空、`Next().Kind == NextInspectWorkflow`がouter Command IDを指す |
| C. Workflow実行中 | outer running、`workflow.reviewed.execute` running | `workflow.reviewed.execute`のLedgerをinspectしrunning |
| D. Plan apply失敗 | outer failed／partial_failure、`ceo_plan.apply` failed、`workflow.reviewed.execute` child不在 | outerのFailure.Code/Stageが`ceo_plan.apply`のものと一致、Session未変更 |
| E. Workflow側失敗 | outer partial_failure、`ceo_plan.apply` succeeded、`workflow.reviewed.execute` partial_failure/failed | outerのFailure.Detailsが`workflow.reviewed.execute`のEnvelopeと一致 |
| F. 完了 | outer succeeded、両child succeeded | `Session.State == StateCompleted` |

`Record.Next()`はこの判定を`NextInspectWorkflow`（既存Kind、`Commands`に該当Command参照を含む）として表現し、**「実行中/失敗どちらであっても人間の確認を促す」既存のsemanticsをそのまま再利用**しました。新しいNextActionKindは追加していません（JSON Contract互換性: 既存clientは`interaction.workflow.execute`が省略されているだけであり、新しいenum値を認識できずに失敗するリスクはありません）。

### 8. chain continuation vs retry（section 13/16）

「Plan apply succeeded、workflow childがまだ一度もclaimされていない → 初めてworkflow childを開始する」ことは、`ExecuteInteractionPlanApproveAndExecute`が**同一のouter Command実行の中で、まだ誰も試みていない子を初めて実行する**ことであり、既存原則が禁止する自動retryではありません。retryが禁止するのは「一度失敗したCommandをGoが勝手にもう一度実行すること」であり、これは今回一切実装していません。

- child（`ceo_plan.apply`または`workflow.reviewed.execute`）が失敗した場合、`ExecuteInteractionPlanApproveAndExecute`はその場で処理を終了し、outer Ledgerレコードへ失敗を記録して返します。同じouter Command IDを再送すれば、既存のreplay機構（`claimWorkspaceCommand`→`replayDurableCommand`）がキャッシュ済み結果をそのまま返すだけで、Provider呼び出しやchild実行は再度行われません（`TestInteractionPlanApproveAndExecuteReplayNeverCallsProviderAgain`で固定）。
- crash後にPlan apply succeeded／workflow未着手の状態（case B）から復旧する唯一の明示的な手段は、**既存の標準`interaction.workflow.execute`コマンドを人間／operatorが直接呼ぶこと**です（Session状態は既に`StateReadyToExecute`のため、この標準コマンドはそのまま機能します）。`interaction.plan.approve_and_execute`を同じPlanに対して再送すると、Plan applyの子は新しいdeterministic IDで再度試行され、既存のProject名衝突検知などの構造的な保護によって安全に拒否されるため、意図せずPlan applyが二重実行されることはありません。

### 9. no automatic retry（section 12）

`ExecuteInteractionPlanApproveAndExecute`にも`runInteractionWorkflowChain`にも、失敗したchildを内部でループしたりもう一度呼び出したりするコードは一切存在しません。すべての失敗経路は、失敗した時点でその場からouter Ledgerへ結果を記録して即座にreturnします。

### 10. partial failure（section 11 E, section 15）

child失敗のFailureEnvelopeは常にforwardされ、再分類されません。

- `ceo_plan.apply`失敗: `ceoApplyCommandFailure(applyErr)`（既存の純粋関数、`ceo_plan.apply`自身のLedgerが記録したものと同一の入力から同一の分類を導出）をそのままouterのfinish呼び出しへ渡します。
- `workflow.reviewed.execute`失敗: `runInteractionWorkflowChain`が返す`*failure.Envelope`は、`workflowFailure(...)`（既存、無変更）がchildの`RecordedCommandError.Envelope`をコピーして構築したものです。

### 11. FailureEnvelope forwarding実装上の発見と修正

本Checkpointの実装過程で、既存の`finishDurableCommandRecord`に潜在的な不具合を発見しました。`errors.Join(commandErr, &RecordedCommandError{...})`という既存の実装順序では、`commandErr`自体が既に別のchildの`*RecordedCommandError`を内包している場合（今回新設したchain継続がまさにこのケースです）、Goの`errors.As`はJoinツリーを深さ優先で辿り、**新しく構築したはずのCommandの分類ではなく、より内側にある古いchildの分類を先に見つけてしまう**ことを確認しました（`/tmp`での再現実験、および本Checkpoint固有の`PROVIDER_CONFIGURATION_REQUIRED`ケースで`Partial`値の不一致として顕在化）。

これは`errors.Join`の引数順序を`errors.Join(&RecordedCommandError{...}, commandErr)`へ反転させることで修正しました（`command_ledger.go`、全durable Command共通のこの1箇所のみ）。`errors.Is`によるsentinelエラー判定はJoinツリー全体を探索するため順序に依存せず、既存の全テストスイート（race含む）が無変更で通ることを確認済みです。この修正により、child→outerの多段forwarding全体（本ADRの新しいchainだけでなく、既存のReviewed Workflow→Interaction Workflowのforwardingも含む）が、より正確に「直近のCommand自身の分類」を返すようになりました。

### 12. standalone operator commands維持

`interaction.plan.apply`・`interaction.workflow.execute`・`interaction.plan.generate`はいずれも削除していません。内部実装（`ExecuteInteractionPlanApply`、`runInteractionWorkflowChain`を共有する`ExecuteInteractionWorkflow`、`executeInteractionPlanGenerationCommand`を共有する`ExecuteInteractionPlanGeneration`）も、標準経路としての動作を一切変更していません。operator CLI（`interaction-plan-apply`、`interaction-workflow-execute`、`interaction-plan-generate`）からも引き続き個別に呼び出せます。新設した`interaction-plan-approve-and-execute` CLI操作は、これらに加わる形で追加しました。

### 13. Public Beta contract

`httpapi.publicBetaCommandOperations`へ`"interaction.plan.approve_and_execute"`を追加しました。既存の`interaction.plan.apply`・`interaction.workflow.execute`は、通常のWeb UXからはNext()経由で到達しなくなりますが、operator／crash-recoveryの安全弁として意図的にallow-listへ残しています（危険なoperator専用操作の新規公開は行っていません）。

### 14. browser close／network断への耐性

`interaction.plan.approve_and_execute`は既存の`Prefer: respond-async`機構（`httpapi/async_command.go`、無変更）でdispatchされます。async実行は`context.Background()`から派生した、HTTPリクエストのcontextから独立したcontextで動くため、browserが閉じてもGo daemon側のgoroutineはchain全体（Plan apply→Workflow実行）を最後まで継続します。既存のmobile Interaction Continuity（ADR-0032）の仕組みをそのまま利用しており、新しい継続機構は追加していません。

## 明示的に対象外（本Checkpoint）

capability field追加、AssignmentPolicy新設、required_role削除、Conversation Projection redesign、Slack風UI／@mention UI、graphical AI employees、history削除、UI styling変更、Browser Acceptance Gate（Node/Playwright）の更新、push、PR、commit。

## Consequences

- 通常成功パスでのCEO操作は、依頼送信（1）とPlanの内容確認（1）の2回に減りました。承認自体は減らさず、機械的に確定している中間ステップ（plan_generation_approval_required、interaction.plan.apply単独承認、interaction.workflow.execute単独承認）だけを1つの明示承認へ畳み込みました。
- Command Ledgerの粒度、partial failure診断、Recovery境界は一切変更していません。既存のstandalone commandがそのまま緊急時の手動再開手段として機能します。
- `finishDurableCommandRecord`のerrors.Join順序修正により、多段chain全体でのFailureEnvelope forwardingがより正確になりました（既存のReviewed Workflow経路にも恩恵があります）。
- `interaction.Record`のJSON Contractに`pre_authorized_workflow_command_id`（`Turn`への追加、additive）が加わりました。既存clientはこのfieldを無視でき、既存の`Turn`構造を破壊しません。
- `Record.Next()`が`plan_approval_required`で返す`operation`文字列が変わりました（`interaction.plan.apply` → `interaction.plan.approve_and_execute`）。`next.operation`を動的に読む既存clientの設計と整合しており、ハードコードされたoperation名に依存するclientだけが追従作業を必要とします。
- `capability`ベースのAssignment、`AssignmentPolicy`、Conversation Projectionの拡張は引き続き将来の対象として記録され、本Checkpointでは着手していません。
