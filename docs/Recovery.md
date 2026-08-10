# Durability / Recovery Foundation

## 目的

Recovery foundationは、process停止やpartial failure後のVaultを読み取り、何がcommit済みかを診断し、安全性を証明できる最小操作だけを明示承認で実行します。自動retry、rollback、artifact adoption、Event replayは行いません。

設計判断は[ADR-0020](adr/ADR-0020-explicit-recovery-foundation.md)、各commit pointはADR-0008〜0018を参照してください。

## 再起動後に観測する証拠

| 状態 | 観測可能な証拠 | 判定 | Foundationの対応 |
|---|---|---|---|
| process途中停止 | 進行中Task、Deliverableなし | Provider結果の有無は再構成不能。実行未完了は確定 | failure reasonを明示した`fail_and_hold_task` |
| Deliverable後／Complete前 | 進行中Task、identity一致する唯一のDeliverable | Deliverable commit済み、Complete未commit | `complete_task` |
| Task保存後／Event・Audit失敗 | Task Versionは確定、期待Auditなし | Event未発行とsubscriber失敗は区別不能 | `unverifiable`として診断。replayしない |
| Review JSON後／Markdown前 | valid canonical JSON、projectionなし | Review factは成立、表示はpartial | 診断のみ。JSONから元Markdownを生成しない |
| Markdownのみ | projectionあり、canonical JSONなし | canonical factが確認不能 | criticalとして拒否 |
| Review Event／Audit失敗 | canonical JSON、期待Auditなし | publication段階は区別不能 | 診断のみ。Eventを再構築しない |
| Revision intent後／Task前 | valid immutable intent、Taskなし | intent commit済み | 診断のみ。Task ID/adoptionを推測しない |
| Revision Task後／Event・Audit前 | intentとTask、期待Auditなし | Task commit済み、publication段階は不明 | 診断のみ |
| completed Task／Deliverableなし | completed Taskだけ | commit ordering違反または証拠欠落 | criticalとして拒否 |
| stale Version／CAS | planの期待Versionと現行Versionが不一致 | plan後に状態変更 | staleとして副作用なしで拒否 |
| staging／temporary残存 | 既知prefixのfile／directory | crash残骸かactive所有か不明 | 診断のみ。削除しない |
| Command claim後／outcome前 | valid `running` Command Ledger record | 実行中かcrashかは区別不能 | `command_incomplete`として診断。自動resumeしない |
| Organization projection不整合 | canonical Employee inventoryとprojection差分 | 既存Go同期で再計算可能 | `organization-sync-plan`と明示execute |

## 運用フロー

```text
workcairn recovery-inspect
    ↓ read-only report
workcairn recovery-plan --task ... --action ...
    ↓ versioned plan + evidence SHA-256 + expected Task Version
人間が証拠とactionを確認
    ↓
workcairn recovery-apply --plan-file ... --approved
    ↓ plan再生成・完全一致検査 → TaskService → EventService → Audit subscriber
```

例：

```bash
workcairn recovery-inspect --vault /path/to/vault --project 'Project名'
workcairn recovery-plan --vault /path/to/vault --project 'Project名' \
  --task TASK-001 --action complete_task > recovery-plan.json
workcairn recovery-apply --vault /path/to/vault --project 'Project名' \
  --plan-file recovery-plan.json --approved
```

中断実行を失敗・保留として確定する場合は、`--action fail_and_hold_task --recovery-reason '確認済み理由'`を指定します。

## 安全性

- inspect／planはVaultを書き換えません。
- applyは`--approved`を確認する前にVaultやplan fileを読みません。
- apply直前に証拠を再読込し、承認planとの完全一致を要求します。
- Task更新はexpected Version付きTaskService APIからCASを使います。
- Task lifecycle EventはTaskServiceだけが生成し、Auditはsubscriberだけが保存します。
- canonical artifactを削除、上書き、別処理の結果としてadoptしません。
- 実行結果はfailure／hold／Task commitのpartial stateを保持し、成功へ丸めません。

## 現在の限界

このfoundationは診断と2つの安全なTask recoveryだけです。Event再配送、Review projection再構成、Revision intent reconciliation、residual削除は自動化しません。ADR-0021の主要な副作用Command Ledgerはproject scopeとworkspace scopeのterminal outcomeを副作用なしで返しますが、`running` commandを自動resumeしません。
