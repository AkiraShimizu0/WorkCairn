# ADR-0015: Employee rename intentを先行保存しfile renameをIdentity commit pointとする

## Status

Accepted

## Context

社員名は表示情報、社員IDは永続参照です。しかし現在のEmployee名は`社員/<氏名>.md`のfilenameで確定し、本文、Workspace State、Project内の構造化表示参照、Identity Historyにも投影されます。Python `EmployeeRenameService`は対象全fileをbackup後に順次置換し、失敗時に逆順rollbackしますが、複数directory・複数fileを単一filesystem transactionにはできず、process crash中のrollback完了を保証できません。

## Decision

v0.3のGo renameは1社員ずつ、read-only planと明示承認付きexecuteで扱います。batch renameは候補全体のpolicy検査と複数Identity commitを必要とするため別フェーズです。

executeは組織単位lock内でplanを再構築し、次の順序でcommitします。

```text
immutable rename intent JSON
→ 社員Markdown filename rename
→ 社員Markdown本文projection
→ Workspace State projection
→ Project内の構造化表示参照projection
→ Identity History projection
```

intentはemployee ID、旧氏名、新氏名、理由、承認時刻、更新対象と除外対象を固定し、既存intentを上書きしません。`社員/<旧氏名>.md`から`社員/<新氏名>.md`への同一directory renameをIdentity nameのcommit pointとします。社員IDとEmployee Markdown内容は維持され、filename以外から新しいIdentityを推測しません。

canonical rename後のprojection失敗はpartial failureです。新filenameを旧名へ自動rollbackせず、完了済みstageと未完了projectionをResultへ返します。保存済みintent、historical Deliverable、Review、Revision、Audit、Decisions、Progress、backupは削除・書換えません。自由文章中の旧氏名はIDとの対応を確認できないため変更せず、位置だけをplanに残します。

現在inventoryが要求した新氏名で、旧filenameが存在しない場合は`already_applied`としてread-onlyに報告できます。ただし不足projectionの修復や既存intentのadoptは行いません。自動retry、reconciliation、batch transaction、crash recoveryはv0.4へ延期します。

Projectの構造化表示参照は、同じtable row、JSON object、または空行区切りblock内にemployee IDと旧氏名が共存する場合だけ更新します。`Tasks.md`はADR-0008のtable digestとmanaged metadataをTaskStoreだけが更新するためrename projectionから除外します。Task担当はemployee IDが正本であり、Task状態・Task lifecycle Event・managed Task metadataは変更しません。

## Consequences

- Identity renameの成立点と後続projection失敗を区別できます。
- crash時に不完全なrollbackで証拠を消す危険を避けます。
- Pythonのbackup/rollback result shapeとは意図的差異があり、Go gatewayはpartial commit fieldsを公開します。
- batch renameはADR-0017、ID repairはADR-0016で拡張します。projection reconciliationはv0.4の残存責務です。
