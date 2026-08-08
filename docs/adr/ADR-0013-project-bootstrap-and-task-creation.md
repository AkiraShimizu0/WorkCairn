# ADR-0013: Projectをdirectory単位でbootstrapしTask作成をTaskServiceへ集約する

## Status

Accepted

## Context

Python `ProjectManager.create_project`は4個のMarkdownを順次作成し、`add_task`は`Tasks.md`の5列表だけを書き換えます。ADR-0008導入後、表だけを変更するPython writerはmanaged metadataのdigestを壊すため、通常Task作成を製品経路に残せません。またProject初期化の途中失敗を公開directory内の部分状態として見せないcommit pointが必要です。

## Decision

Project bootstrapはread-only planと明示承認付きexecuteに分けます。executeはVaultの`プロジェクト`directory内に非公開staging directoryを作り、`Project.md`、managed metadata v1付き`Tasks.md`、`Decisions.md`、`Progress.md`をすべて書込み・file sync・directory syncした後、Project directory名へのrenameで一度だけ公開します。renameがProjectのcommit pointです。

同名Projectまたはmanaged fileが存在する場合はadopt、merge、上書きをせず拒否します。協調するGo process間は`プロジェクト/.workspace-os-projects.lock`で直列化します。rename前の失敗はstaging directoryを削除し、公開状態を変更しません。rename後の親directory sync失敗はProjectが既に見えている可能性を持つpartial failureとして返し、自動削除しません。crash recovery、staging adoption、idempotency、reconciliationはv0.4へ延期します。

`Project.md`は既存Python互換の表示と概要sectionを維持し、永続参照用`project_id`をfrontmatterへadditiveに保存します。Python parserが未知frontmatter fieldを無視できるためJSON Contract v1とlegacy read compatibilityを壊しません。

bootstrap後の通常Task作成は次の順序に限定します。

```text
read-only Task creation plan
→ explicit approval
→ Organization inventoryでassignee IDを検証
→ TaskService.Create
→ task.created Event
→ Audit subscriber
```

Task IDはmanaged `Tasks.md`の一貫したsnapshotからGo Task Domainが決定します。Task状態とTask lifecycle Eventの所有者はTaskServiceだけです。Vault TaskStoreは5列表とmanaged metadataを同じatomic replacementで保存し、Project bootstrap Adapter、Organization Adapter、CLIはTask状態を直接書きません。

Task commit後のEvent/Audit失敗はTaskを削除しないpartial publication failureです。既存Task、既存Project、stale planを推測で再利用しません。Command Ledger、Outbox、自動retryは導入しません。

Python `ProjectManager`は公開API互換とlegacy/referenceとして残しますが、managed Projectの通常Task writerには使用しません。Project dependency metadataを含むCEO plan全体のtransaction化は本ADRの範囲外であり、通常Project bootstrap/Task作成cutover後の残存Python責務として明示します。

## Consequences

- Projectの4 managed fileは全部見えるか、Project自体が見えないかの明確な公開境界を持ちます。
- 最初からADR-0008形式の`Tasks.md`を作るため、暗黙migrationなしでGo TaskStoreを利用できます。
- 通常Task作成はTaskService/Event/Auditを迂回できなくなります。
- Pythonは新形式へ書き込まず、read compatibilityだけを維持できます。
- filesystemを跨ぐatomicity、非協調writerとの競合、crash reconciliationは保証しません。
