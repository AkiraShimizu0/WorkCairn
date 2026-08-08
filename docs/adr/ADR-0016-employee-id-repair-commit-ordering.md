# ADR-0016: Employee ID repair intentを先行保存しEmployee Markdownを順次commitする

## Status

Accepted

## Context

Employee IDは永続参照ですが、legacy Vaultには複数Employee Markdownが同じIDを持つ状態があります。重複中はIDだけでは対象社員を一意に選べず、ProjectやTaskの既存参照が重複社員のどちらを意図したか推測できません。Python `Organization`はfilename順で最初の社員にIDを残し、後続社員のEmployee Markdownだけを書き換えます。複数fileのrollbackはprocess crashを越えて保証できず、Workspace StateやProject表示参照も更新しません。

## Decision

Go v0.3はPythonと同じ決定的なrepair planを生成します。Employee filename順で最初の社員が現在IDを維持し、後続社員に同prefixの未使用連番を割り当てます。採番時はEmployeeだけでなくWorkspace Managerと予約IdentityのIDも使用済みとして扱います。

planはread-onlyです。executeは明示承認と、callerが確認したrepair一覧の完全一致を要求し、組織lock内でinventoryから再計算します。planが変化していた場合は一切書かず拒否します。

commit順序は次の通りです。

```text
immutable Employee ID repair intent JSON
→ 対象Employee Markdownをfilename順にatomic replacement
→ Workspace StateのID＋氏名一致row projection
→ Project内のID＋氏名が同じ構造にある表示参照projection
```

各Employee Markdownのfrontmatter ID置換を、その社員の新IDに対するcanonical commit pointとします。intentはrepair一覧、承認時刻、変更対象を固定し、上書きしません。複数Employeeの途中で失敗した場合は、commit済み件数をpartial failureとして返し、自動rollback・削除・別planのadoptを行いません。

Workspace StateはIDと氏名が完全一致するrowだけを更新します。Projectは同じtable rowまたはJSON objectに旧IDと対象氏名が共存するときだけ投影します。自由文章、historical artifact、Audit、Deliverable、Review、Revisionは変更しません。`Tasks.md`のassignee IDは重複中に対象を特定できず、ADR-0008 managed metadataの所有境界もあるため変更しません。旧IDはfilename順で残した社員の正規IDとして存続します。

retry、adoption、reconciliation、複数file transaction、crash recoveryはv0.4へ延期します。

## Consequences

- Pythonと同じrepair候補を人間が事前確認でき、Go writerへ安全にcutoverできます。
- IDだけの曖昧な参照を推測更新しません。
- intent後または一部Employee commit後の失敗は明示的partial stateになり、証拠をrollbackで失いません。
- Python `Organization.build_id_repair_plan/apply_id_repair_plan`は公開互換referenceとして残せますが、通常repair経路では使用しません。
