# ADR-0014: Employee MarkdownをWorkspace State projectionより先にcommitする

## Status

Accepted

## Context

社員Identityの正本は`社員/<氏名>.md`です。一方、`会社/Workspace State.md`の社員表と部署表は運用表示であり、採用時には両方を更新します。2ファイルを通常filesystem上でatomic transactionにできないため、commit順とpartial failureを明示する必要があります。

## Decision

採用はread-only planと明示承認付きexecuteに分け、氏名policy、全IdentityのID一意性、必須field、Workspace State構造をwrite前に検査します。

executeは既存ファイルを上書きしないatomic createでEmployee Markdownを先にcommitします。このcommitを社員Identity成立のcommit pointとし、その後にWorkspace Stateの社員表・部署表をEmployee inventoryから再生成してatomic replacementします。

Employee Markdown成功後のWorkspace State失敗はpartial projection failureです。成立済みEmployeeを自動削除・rollbackせず、canonical/projection commit状態を返します。再実行時に既存Employeeを推測でadopt、上書き、削除しません。明示reconciliation、idempotency、複数file transaction、crash recoveryはv0.4へ延期します。

Workspace Stateはhuman-readable projectionであり、Employee ID、部署、役割、model、statusの正本ではありません。既存Manager行と社員の現在作業表示はID・氏名が一致する場合だけ維持し、不明値を推測しません。

Go Organization DomainはVault path、Markdown、`.env`、Providerを知りません。Vault Employee AdapterはTask状態を変更せず、Task lifecycle Eventも発行しません。改名は参照更新範囲と履歴を伴う別の複数file operationなので本ADRの対象外です。

## Consequences

- 採用済みなのにcanonical Employeeがない状態を避けます。
- projection失敗を成功や未採用として誤表示しません。
- Python `Employee.save`のrollback semanticsとは異なりますが、immutable factとpartial failureを優先し、Python legacyへ新ルールを追加しません。
- 本ADR時点では改名とID repairを対象外としました。後続のADR-0015/0016/0017がそれぞれ単一rename、ID repair、batch renameのGo境界を定義します。
