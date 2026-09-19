# ADR-0073: Guided Recovery Inspection — read-only HTTP projection of ADR-0020's Recovery Report

Status: Accepted

## Context

[docs/ROADMAP.md](../ROADMAP.md)の「Next 1 — Guided Recovery Inspection」は、Local Web UIのattention表示が現在outer／child Command IDとLedger stateまでしか示せていないことを課題とし、既存ADR-0020のRecovery snapshot／finding／planをread-only HTTP projectionとして公開することを次の一歩と定めています。

ADR-0020は`go/internal/recovery`、`go/internal/service/recovery_service.go`、`go/internal/process/recovery.go`にcanonicalな`Snapshot`→`Report`／`Finding`モデルをすでに定義していますが、これまで`workcairn` CLI（`recovery-inspect`／`recovery-plan`／`recovery-apply`）からしか到達できず、`go/internal/httpapi`は一切importしていませんでした。

M-RECOVERY-1a〜1eは複数回のCodex focused design reviewを経ましたが、review全体としては`app.js`側のasync ownership／invalidation（busy／loading所有権、navigation早期invalidation、explicit refreshとsilent pollingのcontext-version区別）が未解消のまま`NO-GO`で終了しています。一方、backend（safe read-only projectionの型境界、closed validation、raw ProjectNameのpre-I/O検証、HTTP contract）に関する設計論点はこのreview過程で解消済みでした。この状況を受け、WorkはbackendとUIの2つのvertical sliceへ分割する判断を行い、本Checkpoint（M-RECOVERY-3A）ではCodexが設計上妥当と判定済みのbackend read-only sliceだけを実装対象としました。UI（`app.js`、CSS／HTML、Browser test、`inspectCommands`、busy／loading、navigation／polling）は本Checkpointでは一切変更しておらず、M-RECOVERY-1a〜1eで指摘された未解決findingを閉じたうえで、別Checkpointとして改めて実装します。UI実装（M-RECOVERY-4C）とその後のfocused correction（M-RECOVERY-4D.1、4D.3）を経て、M-RECOVERY-4D.4のCodex final focused re-reviewがP0〜P3 0件・Open Questions 0件のGOで完了し、本ADRのStatusはM-RECOVERY-4D.5で`Accepted`へ昇格しました。

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

### 7. UI integration and async ownership contract（M-RECOVERY-4C）

`go/internal/httpapi/web/app.js`が、既存の per-Session Command診断（"詳細を確認"／"処理を再確認"）へRecovery Inspectionをread-onlyで追加しました。CSS／HTML／既存fixture／backend Go source／HTTP JSON Contractは変更していません。設計はM-RECOVERY-1a〜1e、M-RECOVERY-4A〜4B.5の複数回のCodex focused design reviewを経て閉じたものを、そのまま実装しています。

- **Pure preflight**: `buildInspectionRequest(mode, {record, next, error})`はglobal stateを一切読まないpure functionで、eligible（`next.kind`が`inspect_workflow_recovery`／`inspect_action_recovery`、canonical `next.commands`／`next.project_name`を正本とする）とineligible（既存remembered-errorの単一workspace Command fallback、`error.command_id`のみを根拠とし、Recovery GETは一切行わない）の両modeを共通validationのうえで閉じたcontract objectへ検証します。malformedな入力はfetch 0件でcommon terminal rendererの固定errorへ落とします。
- **Terminal renderer**: `renderInspectionTerminal(contentNode)`がsuccess／preflight rejection／Command failureの3経路を集約し、active card、"閉じる" quick reply、composer state、scroll stateを一括で設定します（`replaceChildren`のみ、loadingが残ることはありません）。
- **Recovery response validator**: `validateRecoveryInspectionView(raw, expectedProjectName)`がbackendの`RecoveryInspectionView`契約と1対1で対応するpure closed validatorです。要求したProject名との完全一致も検証し、不一致はresponse全体を拒否します。malformed Findingが1件でもresponse全体を拒否し、部分表示はしません。
- **Async ownership**: `state.refreshBarrier`が全refresh（silent／explicit）共通のsingle-flight guardです（`if (silent && state.refreshBarrier) return;`）。Inspection自身は`state.inspectionSequence`／`state.inspectionActive`／`state.inspectionMode`／`state.inspectionContext`で所有権を管理し、DOM commit直前に`awaitLatestRefreshBarrier()`で最新のrefreshが解決するのを待ってから所有権を再確認します。Silent refreshは、open inspectionのcontextが変化した場合だけ、`state.record`／`state.next`への代入より前にinvalidate＋surface clearします。Ineligible inspectionのcontext再比較は、`state.inspectionContext.commands[0].commandId`という既に検証済みの単一Command IDだけを根拠とし、この不変条件（array・要素数1・canonical文字列）が成立しない場合もcomparisonをskipせず、fail-closedでinvalidateします。
- **Inspection never touches global busy**: `setBusy`／`showError`は一切使用せず、local error copyだけを`ui.activeCard`へ表示します。既存の他処理のglobal busyへ一切干渉しません。
- **Scope外のまま**: Recovery Plan preview、Recovery apply、自動修復、retry、新しいProvider／Keychain経路は追加していません。新規endpointは追加していません（既存の`GET /v1/projects/{project_name}/recovery-inspection`だけを使用）。

### 8. Focused correction（M-RECOVERY-4D.1）

Codex implementation reviewのP1 2件／P2 6件を受け、以下をfocused correctionとして修正しました（4ファイルのみ、backend変更なし）。

- **`restoreDurableFailure`のstrict preflight統合**: reload時の durable failure復元が、`next.commands`を直接loopしていた旧実装から、`inspectCommands`と同一の`buildInspectionRequest("eligible", {record, next})`を経由する実装へ変更されました。preflightがnullを返す場合はCommand GET 0件でreturnし、raw `next.commands`への直接アクセスは一切行いません。呼び出しは`restoreDurableFailure(record, next)`とrecordを明示的に渡す形へ変更しました。
- **Post-unarchive refresh**: `confirmUnarchiveSession`のsuccess handlerが`refreshCurrent(true)`（silent）から`refreshCurrent()`（explicit）へ変更されました。silent refreshはbackground pollの single-flight guard（`if (silent && state.refreshBarrier) return;`）でskipされ得るため、unarchive直後の状態表示が古いarchived stateのまま残るraceがありました。explicit refreshはこのguardの対象外であり、常に新しいsequence／barrierを確立してpreceding pollをstale化します。
- Browser test（`tests/browser/recovery-inspection.spec.mjs`）を、6秒／11秒の固定real-time waitを排したdeterministic pollingトリガー（capturedinterval callback）、global busy ownershipの実flow検証、restore経路のCommand GET 0件証明、post-unarchive foreground refresh non-regression test、silent context-change coverage、stale finally／same-Session reopenのrace testを追加してcloseしました。

### 9. Completion-boundary focused correction（M-RECOVERY-4D.3）

M-RECOVERY-4D.2のCodex reviewが残した最後のP2（stale response releaseとold `finally`／new ownerの順序を固定300ms観測窓で代用していた2 test）を、Browser test側だけのfocused correctionでcloseしました。backend／`app.js`は変更していません。

- **Test-only click-handler completion capture**: `installClickHandlerCapture`が`page.addInitScript`で`EventTarget.prototype.addEventListener`をoverrideし、button要素自身が持つ最新の`"click"`listenerを`__wcLastClickHandler`へ追加的に記録します。実際の`addEventListener`自体の登録・発火挙動は一切変更せず、production sourceへのtest accessorも追加していません。
- **Invocationごとのfifo completion Promise**: `wrapOnClickCompletion`が対象buttonの既存click handlerを、同じ`this`／eventで呼び出しreturn／throw semanticsを変えない薄いwrapperへ置き換え、呼び出しのたびに独立したcompletion Promiseを`window`上のtest固有keyへFIFOで積みます。同一buttonへの複数click（本来のclickとduplicate click試行）が1つのPromiseを共有・競合しないようにするためです。
- **Stale handler／old `finally`の決定的完了境界**: `awaitOnClickCompletion`が該当click呼び出しの完了（`inspectCommands`自身の`finally`によるownership解放を含む）を直接awaitします。duplicate click検証だけは、guardが働けばmicrotask内で解決するという既知の同期特性を使い、`awaitOnClickCompletionExpectingNoFetch`が短い実時間boundとのraceで早期に明確失敗させます。
- **Fixed wait排除**: 対象2 testから`page.waitForTimeout(300)`を除去し、network response eventだけでなくold async onclick handlerの`finally`完了までをawaitしてからnegative assertionへ進む構成へ変更しました。
- **Production sourceへのtest hookなし**: `state`モジュールを公開せず、raw application stateへも書き込みません。instrumentationはpage/test scope限定で、test終了時に破棄されます。

## Consequences

- Recovery診断のcanonical sourceは引き続きADR-0020の`recovery`パッケージのみです。HTTP層は新しい診断ロジックを一切持たない、純粋なprojectionです。
- `Detail`／`Problem`／`References`が将来のいかなる`SnapshotReader`実装によっても、このHTTP endpoint経由で漏洩することは構造的にありません。
- 既存`recovery-inspect`／`recovery-plan`／`recovery-apply` CLI operationの挙動・contractは一切変更していません。
- Recovery Plan preview、Recovery apply、自動修復、retry、Provider／Keychain操作はこのADRのscope外です。追加する場合は別ADRで、明示的なdigest／Version承認を伴う別Checkpointとして設計します。
- UI（`app.js`）統合はM-RECOVERY-4Cで実装済みです。既存Local Web UIの`inspectCommands`（新シグネチャ`inspectCommands(mode, inputs)`へ変更）、`renderAttention`、`renderRememberedError`、`clearActionSurface`、`refreshCurrent`を、§7記載のasync ownership契約に従って拡張しました。M-RECOVERY-4D.1で、`restoreDurableFailure`のstrict preflight統合とpost-unarchive foreground refreshを§8記載のとおりfocused correctionしました。M-RECOVERY-4D.3で、§9記載のとおりBrowser test側のcompletion-boundary focused correctionを行い、M-RECOVERY-4D.4のCodex final focused re-reviewでP0〜P3 0件・Open Questions 0件のGOを得ました。

## Rejected alternatives

- **Snapshotをそのまま公開する**: `Snapshot`は`Problem`／`Reference`を含む内部評価用の型であり、公開contractには適しません。
- **`Finding.Detail`をredactionして公開する**: redactionは将来のcontentに対して安全性を保証できません。fieldごと定義しないことで構造的に安全性を保つ方針を採用しました。
- **UI側でunknown enumのfallback labelだけを安全境界にする**: 主たる安全境界はGo側のclosed validation（本ADR §4）です。UIのfail-closed表示は将来のversion skewに対する追加防御であり、唯一の境界ではありません。

## Scope note

本ADRはbackend read-only vertical slice（M-RECOVERY-3A、3B系、Gate済み・commit済み・push済み）と、UI async ownership統合（M-RECOVERY-4A〜4B.5の複数回のCodex focused design reviewを経て閉じた設計をM-RECOVERY-4Cで実装、M-RECOVERY-4D.1とM-RECOVERY-4D.3でfocused correction）の両方を記録します。Recovery Plan preview、Recovery apply、自動修復、retryは引き続きscope外です。M-RECOVERY-4D.4のCodex final focused re-reviewがP0〜P3 0件・test false-positiveなし・Open Questions 0件のGOで完了したため、本ADRのStatusはM-RECOVERY-4D.5で`Accepted`へ昇格しました。実Provider成功や既存Public Beta releaseの完了状態の変更は主張しません — `v1.0.0-beta.1`の公開record自体はこのADRの対象外です。
