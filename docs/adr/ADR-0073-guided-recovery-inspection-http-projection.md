# ADR-0073: Guided Recovery Inspection — read-only HTTP projection of ADR-0020's Recovery Report

Status: Proposed

## Context

[docs/ROADMAP.md](../ROADMAP.md)の「Next 1 — Guided Recovery Inspection」は、Local Web UIのattention表示が現在outer／child Command IDとLedger stateまでしか示せていないことを課題とし、既存ADR-0020のRecovery snapshot／finding／planをread-only HTTP projectionとして公開することを次の一歩と定めています。

ADR-0020は`go/internal/recovery`、`go/internal/service/recovery_service.go`、`go/internal/process/recovery.go`にcanonicalな`Snapshot`→`Report`／`Finding`モデルをすでに定義していますが、これまで`workcairn` CLI（`recovery-inspect`／`recovery-plan`／`recovery-apply`）からしか到達できず、`go/internal/httpapi`は一切importしていませんでした。

M-RECOVERY-1a〜1eは複数回のCodex focused design reviewを経ましたが、review全体としては`app.js`側のasync ownership／invalidation（busy／loading所有権、navigation早期invalidation、explicit refreshとsilent pollingのcontext-version区別）が未解消のまま`NO-GO`で終了しています。一方、backend（safe read-only projectionの型境界、closed validation、raw ProjectNameのpre-I/O検証、HTTP contract）に関する設計論点はこのreview過程で解消済みでした。この状況を受け、WorkはbackendとUIの2つのvertical sliceへ分割する判断を行い、本Checkpoint（M-RECOVERY-3A）ではCodexが設計上妥当と判定済みのbackend read-only sliceだけを実装対象としました。UI（`app.js`、CSS／HTML、Browser test、`inspectCommands`、busy／loading、navigation／polling）は本Checkpointでは一切変更しておらず、M-RECOVERY-1a〜1eで指摘された未解決findingを閉じたうえで、別Checkpointとして改めて実装します。Codex implementation reviewがGOになるまで、本ADRのStatusは`Proposed`のままとします。

## Decision

### 1. 内部Reportと公開viewの分離（二段projection）

`recovery.Snapshot`と`recovery.Report`／`recovery.Finding`は変更しません。新規`go/internal/process/recovery_view.go`が、次の二段構成で安全なpublic viewへ変換します。

- `InspectRecoveryView(ctx, input)`: 既存`InspectRecovery`を呼ぶ前に、raw（trim前）の`input.ProjectName`をcanonical identifierとして検証します。不正なら`InspectRecovery`、Vault reader生成、filesystem readのいずれよりも前に`ErrUnsupportedRecoveryProjection`を返します。既存`InspectRecovery`自体（operator向けCLI `recovery-inspect`が使う関数）はtrim動作を含め一切変更していません。
- `ProjectRecoveryInspectionView(report)`: 純粋関数。`recovery.Report`を受け取り、`RecoveryInspectionView`（`RecoveryFindingView`のスライス）を返します。

### 2. Unsafe free-textの非公開

`recovery.Finding.Detail`（自由記述の診断文）と`recovery.Finding.References`（Vault-relative path）は、`RecoveryFindingView`のfieldとして一切定義していません。redactionやfilteringではなく、構造的にこれらのfieldを読まないことで安全性を保証します。`Finding.Detail`／`Problem`は現在の`service.inspectRecoverySnapshot`実装では固定安全文言ですが、型としては自由文字列であり将来の別`SnapshotReader`実装まで安全性を保証しないため、この構造的な非公開が唯一の安全境界です。Snapshot evidenceの`Problem`／`Reference`（`DeliverableEvidence.Problem`等）はRecovery inspection処理の内部で参照され得ますが、それ自体もpublic viewへは一切コピーされません。

### 3. RelatedIDの理由

`recovery.Finding.TaskID`は常にTask IDとは限りません。`FindingCommandIncomplete`は`CommandEvidence.AggregateID`（Command Ledgerのaggregate識別子）を同じfieldへ渡します（`go/internal/service/recovery_service.go`の該当`finding(...)`呼び出し）。公開viewでは`RelatedID`／`related_id`と改名し、意味の誤認を避けます。

### 4. Closed validation

`ProjectRecoveryInspectionView`は公開前にfail-closedで次を検証し、いずれか一つでも不正なら単一の`ErrUnsupportedRecoveryProjection`を返します。

- `report.SchemaVersion == recovery.SchemaVersion`
- `ProjectName`：non-empty、trim前後一致、CR／LFを含まない
- `TaskCount >= 0`
- `Healthy == (len(Findings) == 0)`
- 各Finding IDがcanonical、non-empty、Report内unique（`RECOVERY-NNN`という現在のprefix／桁数は`recovery.SortFindings`の実装詳細であり固定しません — 保証されているのはReport内でのunique性だけです）
- `Kind`が現在の11値のいずれか
- `Severity`が`warning`／`critical`
- `Certainty`が`confirmed`／`unverifiable`
- `RecommendedAction`が`none`／`complete_task`／`fail_and_hold_task`（既存`recovery.Action.Valid()`を再利用）
- `Recoverable`と`RecommendedAction`の双方向整合（`true`なら非`none`、`false`なら`none`）。これは`go/internal/service/recovery_service.go`の唯一の`Finding{}`構築箇所（`finding(...)`、18呼び出し箇所すべて）を全数確認して得た現行productionのinvariantです。
- `RelatedID`はemptyまたはcanonical

### 5. Additive GET endpoint

```
GET /v1/projects/{project_name}/recovery-inspection
```

`go/internal/httpapi/handler.go`に新規`RecoveryInspector`interfaceと`inspectRecoveryInspection`handlerを追加し、`NewHandler`はexecutorがこのinterfaceを満たす場合だけ登録します（既存`TaskEvidenceInspector`等と同じconditional registration pattern）。`go/internal/httpapi/executor.go`の`ProcessExecutor.InspectRecoveryView`が`executor.vaultRoot`を注入し、public `process.InspectRecoveryView`を呼びます。

### 6. Fixed 422 contract

成功時：HTTP 200、outer `Response.Version = httpapi.ContractVersion`（新しい重複version constは追加していません）、inner `schema_version = recovery.SchemaVersion`。

失敗時（inspection service自体のエラー、projection rejection、存在しないProjectのすべてを含む）：常にHTTP 422、`code: "RECOVERY_INSPECTION_FAILED"`。raw error、raw ProjectName、Vault path、`Detail`／`Problem`／`References`はresponseに一切含みません。`RecoveryRequired`は設定しません — read-onlyなinspection自体の失敗だけでは状態変更Recoveryが必要とは断定できず、既存CLIの`recovery-inspect`もこの意味を提供していないためです。存在しないProjectをhealthy responseへ変換することはありません。

既存`local-access`middleware（`authorizeLocalRequest`）を迂回しません。`--local-network`モードではpairing authorizationが引き続き必要です。

## Consequences

- Recovery診断のcanonical sourceは引き続きADR-0020の`recovery`パッケージのみです。HTTP層は新しい診断ロジックを一切持たない、純粋なprojectionです。
- `Detail`／`Problem`／`References`が将来のいかなる`SnapshotReader`実装によっても、このHTTP endpoint経由で漏洩することは構造的にありません。
- 既存`recovery-inspect`／`recovery-plan`／`recovery-apply` CLI operationの挙動・contractは一切変更していません。
- Recovery Plan preview、Recovery apply、自動修復、retry、Provider／Keychain操作はこのADRのscope外です。追加する場合は別ADRで、明示的なdigest／Version承認を伴う別Checkpointとして設計します。
- UI（`app.js`）統合は別sliceです。既存Local Web UIの`inspectCommands`／`renderAttention`等は本Checkpointでは変更していません。

## Rejected alternatives

- **Snapshotをそのまま公開する**: `Snapshot`は`Problem`／`Reference`を含む内部評価用の型であり、公開contractには適しません。
- **`Finding.Detail`をredactionして公開する**: redactionは将来のcontentに対して安全性を保証できません。fieldごと定義しないことで構造的に安全性を保つ方針を採用しました。
- **UI側でunknown enumのfallback labelだけを安全境界にする**: 主たる安全境界はGo側のclosed validation（本ADR §4）です。UIのfail-closed表示は将来のversion skewに対する追加防御であり、唯一の境界ではありません。

## Scope note

本ADRはM-RECOVERY-3A（backend read-only vertical slice）のみを記録します。UI側の未解決async ownership／invalidation（Codex focused design reviewで指摘されたbusy／loading所有権、navigation早期invalidation、explicit refreshとsilent pollingのcontext-version区別）は別Checkpoint・別ADR更新の対象です。Codex implementation reviewがGOになるまで、本ADRのStatusは`Proposed`のままとします。
