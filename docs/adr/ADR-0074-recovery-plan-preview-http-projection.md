# ADR-0074: Recovery Plan Preview — read-only, safe HTTP projection before explicit Apply

Status: Proposed

## Context

[ADR-0020](ADR-0020-explicit-recovery-foundation.md)は、確定済みのTask／Deliverable evidenceから`recovery.Plan`を再導出し、明示承認後にだけRecoveryを適用するoperator向けCLI境界を定義しています。[ADR-0073](ADR-0073-guided-recovery-inspection-http-projection.md)はRecovery Reportのsafeなread-only HTTP projectionとLocal Web UI表示を追加しましたが、Plan preview、Apply、自動修復、retryは意図的にscope外としました。

次の小さいvertical sliceでは、Humanが変更操作を承認する前に「どのTaskを、どのActionで、どのVersionから変更しようとしているか」と「なぜ現在実行不能か」を確認できるようにします。ただし、内部`recovery.Plan`にはEvidence reference／digest、SourceRevision、失敗理由、承認要否が含まれます。これらをそのままHTTPへ公開せず、入力検証とsafe projectionをProcess境界へ集約する必要があります。

## Decision

### 1. Additive read-only endpoint

次のendpointをadditiveに追加します。

```text
POST /v1/projects/{project_name}/tasks/{task_id}/recovery-plan-preview
```

Requestは`workspace-command.v1`と、closedな`action`（`complete_task`／`fail_and_hold_task`）を必須とします。`reason`はpresence-awareにdecodeし、`null`、非string、unknown field、trailing JSONを拒否します。

このendpointは既存plannerを1回だけ呼ぶread-only previewです。Task／Deliverable／Audit／Command Ledger／Sessionを変更せず、Provider call、Keychain access、retry、fallbackを行いません。executorがpreview capabilityを実装する場合だけrouteを登録する、既存のconditional registration patternを使います。既存local-access middlewareも迂回しません。

### 2. Reason contract and pre-I/O validation

`go/internal/process`は、Vault reader生成・filesystem access・planner呼び出しより前にraw path inputとRequestを検証します。

| Action | `reason` contract |
| --- | --- |
| `complete_task` | absentまたはexact empty stringだけを許可する |
| `fail_and_hold_task` | 必須。non-empty、trim前後一致、UTF-8文字列のbyte lengthが16 KiB以下 |

Project nameとTask IDもtrimによる補正を行わずcanonical valueだけを許可し、Task IDは既存`task.ParseTaskID`を通します。上限値はProcess packageの`MaxRecoveryPlanPreviewReasonBytes`を正本とし、HTTP decodeとProcess境界の双方が同じ値を参照します。

client入力不正はclosed sentinel `ErrInvalidRecoveryPlanPreviewInput`とし、HTTP 400 `INVALID_RECOVERY_PLAN_PREVIEW`へ分類します。Plannerが返したPlanのProject／Task／Action／Reasonが検証済み入力と一致しない場合はclient errorへ戻さず、安全なprojection不能として拒否します。

### 3. Safe Plan projection

`ProjectRecoveryPlanView`は最初に既存`recovery.Plan.Validate()`を実行し、その後に公開境界固有の不変条件をfail-closedで検証します。成功時に公開するfieldは次だけです。

- `schema_version`
- `project_name`
- `task_id`
- `action`
- `task_status`
- `task_version`
- `executable`
- `blocking_reasons`

`EvidenceRef`、`EvidenceDigest`、`Reason`、`SourceRevision`、`ApprovalRequired`はpublic viewのfieldとして定義せず、responseへコピーしません。

`blocking_reasons`はaction別のclosed setと既存Serviceの順序に固定し、unknown、重複、順序違反、別action用の値を拒否します。

- `complete_task`: `task_not_in_progress` → `matching_deliverable_not_confirmed`
- `fail_and_hold_task`: `task_not_in_progress` → `deliverable_present_or_invalid`

空の場合も`null`ではなく`[]`を返します。`failure_reason_required`はpublic blockerにしません。reason不足はpre-I/OのHTTP 400であり、plannerへ到達しないためです。

### 4. Evidence semantics

Completion evidenceと`executable`を双方向条件として扱いません。

- `action=complete_task && executable=true`ならvalid completion evidenceを必須とする既存`Plan.Validate()`を維持する。
- valid evidenceが存在しても、TaskがIn Progressでなければ`task_not_in_progress`により`executable=false`になり得る。このblocked Planは正当なpreviewとして投影する。
- `fail_and_hold_task`ではEvidence reference／digestが空であることを追加検証する。

つまり、projectorはEvidenceの存在から`executable`を逆推論せず、core PlanのAction、Task state、Executable、Blockers、Evidence bindingを独立して検証します。

### 5. HTTP error taxonomy

Handlerは文字列比較ではなく`errors.Is`でclosed sentinelを分類します。

- semantic client input error: HTTP 400、`INVALID_RECOVERY_PLAN_PREVIEW`
- valid inputから生成されたPlanをsafe projectionできない場合: HTTP 422、`RECOVERY_PLAN_PREVIEW_FAILED`
- Project／Task不存在、Vault／snapshot／planner等の内部失敗: HTTP 422、`RECOVERY_PLAN_PREVIEW_FAILED`
- unsupported Content-Type: 既存transport contractのHTTP 415
- request body size超過: 既存transport contractのHTTP 413

400／422 responseはいずれもouter `workspace-command.v1`、`ok=false`、固定`error.code`だけを返します。raw input、存在差、internal error、Vault pathを公開しません。Project不存在とTask不存在は同じ422 bodyになります。

### 6. Apply is a separate future boundary

本endpointのresponseはApply tokenでも承認commitmentでもありません。Recovery apply、自動修復、artifact adoption、retryは実装しません。

将来ApplyをHTTPへ追加する場合は別ADRで、少なくとも次を改めて設計します。

- canonical Planの再導出
- domain-separated commitment／digest
- Task VersionのCAS
- 明示Human承認
- Command Ledger claim-before-effectsとreplay semantics
- stale Planの拒否

preview responseやbrowser stateを、そのまま変更権限として再利用しません。

### 7. Verification boundary

Backend testは次を独立して証明します。

- input不正がplanner／Vault I/Oより前に止まる
- plannerはvalid requestごとに最大1回
- complete／failのpositive Plan、blocked complete＋valid evidence
- action別blockerのclosed set、順序、重複拒否
- public JSONのexact key setとhidden field非露出
- success、400、422、413、415のsafe exact response
- Project／Task不存在のindistinguishable error body
- local-network authorization／intent維持
- temporary Vaultの全entry／bytesが複数preview前後で不変

Browser UIは本ADRの現Checkpointでは実装しません。UI preflight、global busy非干渉、fetch ownershipは後続sliceで実装・reviewします。

## Consequences

- HumanはApply前にread-onlyなTask recovery intentとblockerを確認できるbackend contractを得ます。
- Core plannerを正本のまま再利用し、HTTP層へRecovery判断を複製しません。
- 内部evidence／reason／revision情報は構造的にpublic responseへ入りません。
- 400はclientが送った意味的入力だけ、422は内部Plan／projection／existence failureだけを表します。
- 新しいProvider call、credential経路、永続state、automatic repairは増えません。
- UIが未実装でimplementation reviewも未完了のため、本ADRは`Proposed`を維持します。

## Rejected alternatives

- **内部`recovery.Plan`をそのままJSON化する**: evidence path／digest、reason、SourceRevisionを公開するため不採用。
- **すべて400またはすべて422へ統合する**: client修正可能な入力不正と内部safe-projection failureを区別できず、存在差を安全に閉じる責務も曖昧になるため不採用。
- **reasonをtrimして受理する**: Humanが承認した入力を暗黙に書き換えるため不採用。
- **previewとApplyを同じendpoint／Commandにする**: read-only確認とeffect承認の境界を壊すため不採用。
- **blocked PlanからEvidenceを除去する**: valid evidenceがあってもTask stateによりblockedになり得るcore semanticsを失うため不採用。

## Scope note

本ADRはM-RECOVERY-5Cのbackend vertical sliceだけを記録します。ADR-0073のscopeは変更しません。Local Web UI、Recovery apply、自動修復、retry、Provider、Keychain、candidate／Releaseは対象外です。Backend implementationとCodex focused review、および後続UI sliceが完了するまでStatusは`Proposed`です。
