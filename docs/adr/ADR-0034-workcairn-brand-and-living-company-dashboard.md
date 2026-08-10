# ADR-0034: WorkCairnを製品名としLiving Company Dashboardをread-only projectionとして提供する

## Status

Accepted

## Context

Go Only RuntimeとPublic Beta配布基盤は成立しましたが、旧称`Workspace OS`は内部Architectureを表す一般名であり、「自分専用のAI会社へ仕事を任せる」という利用価値を伝えません。既存Local Web UIもNext Actionを安全に操作できる一方、担当AI、MakerとReviewerの分離、仕事のhandoffを一覧できず、会社が自律的に働いている実感を得にくい状態です。

Public Beta前で外部利用者はいないため、公開surfaceの破壊的renameは今実施できます。一方、製品名と無関係なJSON ContractやVault persistence namespaceまで変更すると、安全性に寄与せず既存fixtureとcanonical evidenceを壊します。

## Decision

### Product identity

正式製品名を`WorkCairn`、conceptを`Your AI company that manages itself.`とします。中心メッセージは「会社は見える。仕事も見える。でも管理しなくていい。」です。Public向けにはagent orchestration implementationではなく、次の体験を先に説明します。

- 自然言語で会社へ仕事を依頼する
- AI社員が計画、実行、独立Review、必要なRevisionを進める
- 人間はclarification、approval、Recoveryなど必要な判断だけを行う
- 成果物とExternal Actionによって現実の仕事を完了する

給与、PIP、機嫌、昇進等の会社simulationを中心機能にしません。AI社員を所有し、責任と仕事の流れが見えることを重視します。

### Rename boundary

Public Beta前に次をWorkCairnへ揃えます。

- UI、README、現行Architecture／運用／release文書のproduct表示
- binaryの`workcairn`、`workcairn-daemon`、`workcairn-core`
- release archiveの`workcairn_<version>_<os>_<arch>`
- 公開予定repository slugとGo moduleの`github.com/AkiraShimizu0/workcairn/go`
- WorkCairn固有Provider／Action環境変数とbrowser-local key

GitHub repositoryの実rename、redirect、Public化は人間の外部操作として別に行います。内部の`Workspace`、`Workspace Kernel`、Workspace root等はArchitecture上の一般概念なので維持します。

次は製品名ではなく安定contract／永続formatなので変更しません。

- `workspace-command.v1`、`workspace-interaction.v1`、JSON Contract v1のoperationとenvelope
- Vaultの`.workspace-os` machine metadata directory
- `workspace-os-task-metadata:v1` marker、lock／temporary filename
- 既存fixtureとAccepted ADR内のhistorical identifier

この区別によりbrand renameをcanonical data migrationへ拡大しません。

### Living Company Dashboard

ADR-0031のthin same-origin clientへ2つのprojectionを追加します。

- `My Actions`: iPhone既定。ServerのInteraction Next Actionが要求する質問、承認、Recoveryだけを最優先表示する
- `Company View`: PC／iPad既定。Organization inventory、Interaction Plan、Workflow evidence、Task evidenceからAI社員、担当、Maker → Reviewer → Revisionのhandoffを表示する

Company Viewは人型の視覚表現を使いますが、office decorationやsimulation stateを持ちません。社員status、担当、Reviewer、Revision、blocked／completedは既存read-only APIから導出し、推測できない担当は`未割当`として表示します。Task遷移、Review verdict、Revision条件、approval rule、Recovery判断をJavaScriptへ再実装しません。

Next Actionが不要な状態では`Your company is working. No action needed.`相当を明示します。必要な状態ではMy Actionsへ誘導し、partial failureを成功表示せず、成立済み記録を保持して自動retryしない理由を平易な文言で説明します。

## Consequences

- Public Betaの製品surfaceと配布名がWorkCairnへ統一されます。
- iPhoneでは判断だけ、広い画面では会社の責任と仕事の流れを楽しめます。
- UI追加は既存Interaction／Organization／evidence APIのprojectionであり、Kernel／Domain／ServiceやJSON Contractを変更しません。
- 旧binary名と旧WorkCairn固有環境変数のcompatibility aliasは提供しません。
- `.workspace-os`等の文字列は意図的に残り、将来変更する場合は独立したstorage migrationとADRが必要です。
- 実repository rename、商標clearance、実機でのブランド／UX確認はPublic Beta公開前の人間作業として残ります。
