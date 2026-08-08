# ADR-0021: Command claimを副作用より先にcommitし同一IDの再送を判定する

## Status

Accepted

## Context

ADR-0020により、process停止後の確定証拠と安全な明示Recoveryは診断できるようになりました。しかし、CLI、将来のHTTP client、daemonがtimeout後に同じTask executionを再送すると、Task CASやimmutable Deliverableだけでは「同じ依頼の再送」か「別の依頼」かを判定できません。Provider呼出し前にprocessが応答を失った場合も、外部callerは実行開始の有無を知りません。

Task execution requestには既に`CommandID`が通っていますが、従来は結果へのtrace fieldであり永続判定には使っていませんでした。HTTP APIやautomationを追加する前に、最も高価で副作用の多い通常Task executionでdurable command identityを成立させる必要があります。一方、Event Outbox、全command共通workflow、既存artifact adoptionを同時に導入すると、現在のcommit pointとRecovery境界を不必要に変更します。

## Decision

### Scope

Command Ledgerは、明示的な`CommandID`が指定された主要な副作用commandへadditiveに適用します。対象は通常Task execution、Review、Revision、CEO plan apply、Project bootstrap、Task create、Task Dependencies、Employee hire／rename／ID repair、Organization sync、Sequential Workflow executionです。既存CLI／公開compatibilityを破壊しないため、既存operationの`CommandID`未指定呼出しは従来経路を維持します。新規Workflow executeとretry可能なHTTP／daemon入口ではCommand IDを必須にします。

各commandは既存ADRのcommit orderingをそのまま実行し、Ledgerはその外側でclaimとterminal outcomeだけを調停します。CEO apply内部のProject／Task writerのように外側commandが一連の副作用を所有する場合、内側writerへ別Command IDを合成せず、外側claimだけを正本にします。

### Commit ordering

```text
explicit approval
→ durable Command claim (running, version 1)
→ existing execution preflight / Task / Provider / Deliverable / Event / Audit
→ terminal Command outcome (version 2)
```

Command claimはTask変更、Provider HTTP、Deliverable保存より先にatomic createします。preflight failureも同じCommandのterminal failureとして保存します。outcomeは`running`から`succeeded`、`failed`、`partial_failure`のいずれかへ一度だけCAS更新し、terminal recordを再更新しません。

outcome保存に失敗した場合、成立済みTask、Deliverable、Event、Auditをrollbackしません。Command claimが`running`のまま残る可能性を持つtyped partial failureとして返し、ADR-0020のRecovery inventoryで観測します。

### Identity and request digest

Command IDは128文字以下の明示IDとし、Project scopeまたはworkspace scope内の永続identityです。Vault pathには直接使用せず、SHA-256 filenameへ写像します。

request digestはcanonical JSONのSHA-256です。通常Task executionではProject ID／name、Task ID、注入時刻、approval source／reference、Execution ID、Provider model、max tokensを含みます。ReviewではReviewer IDとReview versionも含みます。APIキー、secret、Base URL、Vault absolute pathは含めず、Ledgerにも保存しません。

- 同じCommand ID、同じdigest、terminal outcome: ProviderやTask処理を再実行せず、保存済みtyped result／errorを返す。
- 同じCommand ID、異なるdigest: `COMMAND_ID_CONFLICT`として副作用前に拒否する。
- 同じCommand ID、`running`: process crashか実行中かを推測せず`COMMAND_IN_PROGRESS`として拒否する。自動resumeしない。
- terminal failureの再送: 同じfailure resultを返し、新しい実行へ変換しない。人間がRecoveryを確認し、必要なら新しいCommand IDを使う。

### Storage and boundaries

Command Domain／ServiceはVault、Markdown、Provider、Task状態を知りません。Vault AdapterはProject内commandをProject配下の`.workspace-os/commands/<Command ID SHA-256>.json`、Project作成前のcommandとOrganization writerをVault直下の`.workspace-os/commands/<Command ID SHA-256>.json`へ保存します。workspace scopeのrecordは`project_name`へ固定値`workspace`を保存し、Project作成前でもclaimを先行commitできます。これは人間向け表のprojectionを持たないmachine metadataであるため、ADR-0008の5列Tasks表示とは異なりhidden sidecarを採用します。

record作成は既存を上書きしないatomic create、outcomeはfile lock、expected Version、atomic replacementで保存します。resultはtyped execution Resultで、Prompt、APIキー、Provider request headerを含めません。

Command LedgerはTask状態やTask lifecycle Eventを生成しません。TaskService ownership、Deliverable ordering、Review／Revision ordering、Audit subscriber方式は変更しません。

### 延期する事項

- `running` commandの自動resume／abandon
- Event再配送、Transactional Outbox
- 複数commandを束ねるworkflow run ledger
- retention、compaction、remote database
- clientにCommand IDを必須化するversion付きHTTP contract

## Consequences

- 明示Command IDを使う主要な副作用commandは、完了応答の再送でProviderやVault副作用を重複実行しません。
- 異なるpayloadでのID再利用と、結果不明の`running`再実行を安全に拒否します。
- process crash後は自動継続できませんが、重複実行より安全なblocked stateとしてRecoveryから観測できます。
- outcome commit自体が新しいpartial failure pointになります。recordを成功へ推測せず、既存canonical evidenceから診断します。
- Command ID未指定経路とmigration／Recovery等の専用操作はdurable retry保証を持ちません。version付きHTTP入口ではCommand IDを必須化します。
