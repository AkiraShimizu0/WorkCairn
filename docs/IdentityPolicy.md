# Identity Policy

## 目的

IdentityPolicyは、AI社員のIDと氏名が既存組織メンバーと紛らわしくならないように検証します。社員名は表示情報、社員IDは永続参照として扱います。

## 診断対象

`Organization.get_all_identities()`は次を区別したまま返します。

- 社員Markdownの通常社員
- Workspace State.mdの`MGR-*`行
- Organizationへ登録された予約済み組織ID

通常の`get_all_employees()`にはWorkspace Managerと予約Identityを含めないため、社員数や部署人数へ二重計上されません。

## 正規化

- Unicode NFKC正規化
- 半角・全角空白の統一
- 比較時の空白除去
- 日本語の自然な`姓 名`形式の確認

## 判定ルール

| 検査 | level | 採用 | 説明 |
|---|---|---|---|
| ID完全一致 | error | 拒否 | 通常社員、Manager、予約IDを横断して検査 |
| 氏名完全一致 | error | 拒否 | 既存Identityと同一 |
| 正規化後一致 | error | 拒否 | 空白表現だけが異なる名前を含む |
| 日本語姓名形式でない | error | 拒否 | 英数字混在、姓名分離不能など |
| 不正語を含む | error | 拒否 | `optional`、`null`、`未設定`など |
| 同じ名 | warning | 拒否 | 初期ポリシーではwarningでも採用を止める |
| 同じ姓 | warning | 許可 | 警告として返し、運用上の改名候補にできる |
| 高類似名 | warning | 許可 | 既定の類似度しきい値は0.8 |

v0.1.0のリリース時点では、運用上のIdentity整理により全組織で姓・名とも重複0の状態を採用しています。

## 採用時の利用

Recruiterは書き込み前にIdentityPolicyを実行します。完全一致、同じ名、形式不正、不正語を含む候補は拒否します。同姓や高類似の警告は`return_result=True`で呼び出し側へ返せます。

複数候補の採用では、既存社員と候補同士を全件検証してから保存します。1件でもIDまたは氏名に問題があれば、採用処理を開始しません。

## 既存組織の診断

```python
from workspace_ai.identity_policy import IdentityPolicy

audit = IdentityPolicy().audit_all_identities()
print(audit["errors"])
print(audit["warnings"])
```

診断結果にはID重複、完全一致、正規化一致、同じ名、同じ姓、高類似、不正名、改名候補が含まれます。診断だけではVaultを変更しません。

## 安全な改名

通常経路では`WorkspaceRunEmployeeRenameGateway`がGo batch planで次を一括検証し、ADR-0015の単一renameを順次commitします。Python `EmployeeRenameService`は公開互換referenceです。

- 社員IDが社員Markdownと全組織Identityで一意
- 現在氏名が想定どおり
- 新氏名がIdentityPolicyを通過
- 改名先ファイルが存在しない
- Workspace StateのIDと氏名が一致
- 更新するプロジェクト参照でIDと氏名の対応を確認可能

実行時は社員ごとにimmutable intentを先行保存します。途中失敗時は成立済みIdentityを逆順復元せず、完了済みrenameと失敗stageをpartial stateとして返します。社員ID、自由文章、過去のDeliverables、Reviews、Audit Log、Progress、Decisions、Revisions、既存バックアップは変更しません。

改名履歴は`会社/Identity History.md`へ`employee_id`、`old_name`、`new_name`、`renamed_at`、`reason`とともに記録します。
