# WorkCairn Product Naming

Decided: 2026-08-10

## Adopted name

Public Betaの第一正式名称は`WorkCairn`です。

- Product: **WorkCairn**
- Concept: **Your AI company that manages itself.**
- Primary copy: **Your AI company. You make the decisions that matter.**
- 日本語: **あなたのAI会社。必要な判断だけ、あなたがする。**
- Supporting copy: **頼んだら、あとは会社に任せる。**
- Product principle: **会社は見える。仕事も見える。でも管理しなくていい。**

WorkCairnはagentを細かく設定・監督するtoolではなく、自分専用のAI会社へ仕事を任せる体験を提供します。AI社員、担当、MakerとReviewer、Revision、仕事の状態は見えますが、給与、PIP、機嫌、昇進等のsimulationを主役にしません。

## Renamed Public Beta surface

- app／docs／release表示: `WorkCairn`
- primary CLI: `workcairn`
- local daemon／Web UI: `workcairn-daemon`
- JSON Contract executable: `workcairn-core`
- release archive: `workcairn_<version>_<os>_<arch>`
- intended repository slug: `workcairn`
- Go module: `github.com/AkiraShimizu0/workcairn/go`
- product-specific environment prefix: `WORKCAIRN_`

実GitHub repositoryのrename、redirect、Public化はこのrepository内の変更では実行しません。Public Beta公開前にrelease ownerが外部操作として行います。

## Intentionally unchanged identifiers

次は製品名ではなく、既存の通信／永続化contractまたはArchitecture用語なので維持します。

- `Workspace`、`Workspace Kernel`、workspace root
- `workspace-command.v1`、`workspace-interaction.v1`
- JSON Contract v1のoperation／envelope
- `.workspace-os` machine metadata directory
- `workspace-os-task-metadata:v1`と既存lock／temporary filename
- Accepted ADRとMigration History内の当時の名称

詳細な境界は[ADR-0034](adr/ADR-0034-workcairn-brand-and-living-company-dashboard.md)を正とします。

## Checks required before publication

1. 配布予定地域で`WorkCairn`の商標・法人名・近似名を正式にclearanceする。
2. GitHub repository slug、主要domain、package registry、SNS handleを確保または利用可否確認する。
3. app表示名、repository、Go module、archive、Release noteが同じ表記であることを確認する。
4. iPhone／iPad／Macで`WorkCairn`、`My Actions`、`Company View`の初見理解を実機確認する。
5. rename前のbinaryやarchiveをPublic Beta配布物へ混在させない。
