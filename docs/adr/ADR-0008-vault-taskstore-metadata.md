# ADR-0008: Tasks.mdの5列表とmanaged metadataを同一ファイルで永続化する

## Status

Accepted

## Context

Go Task DomainはTaskの正式状態に加えて、楽観的並行制御に使う`Version`、直近の実行失敗を表す`LastFailureReason`、Policy判断を表す`HoldReason`を保持します。現行Vaultの`Tasks.md`は、人間とPython legacy実装が利用する次の5列だけを持ち、これらのGo Domain metadataを保存できません。

```text
ID | タスク | 状態 | 担当社員ID | 作成日時
```

Vault TaskStoreは、Markdownを人間が読める運用データとして維持しながら、process再起動後と複数のGo process間でもVersion/CASを失わない必要があります。また、移行期間中はPython legacy parserが既存5列表を読み続けられる必要があります。

候補は、同一`Tasks.md`内のmanaged HTML comment、sidecar JSON、5列表への列追加です。

## Decision

### 保存形式

現行の5列表を維持し、`Tasks.md`の表より後、ファイル末尾にversion付きのmanaged HTML commentとJSON metadataを1個だけ配置します。

- comment markerは`workspace-os-task-metadata:v1`とします。
- JSONにも`schema_version: 1`を持たせ、markerとpayloadのversionを一致させます。
- JSONの`tasks`はTask IDをkeyとし、`version`、`last_failure_reason`、`hold_reason`、`table_digest`を保存します。
- `table_digest`はID、タイトル、状態、担当社員ID、作成日時の意味内容から計算します。空白など表示上だけの差は無視し、Taskの意味が変わる編集は検出します。
- metadata blockはGo TaskStoreだけが管理します。人間やlegacy実装が直接編集する対象ではありません。

表は人間可読な表示であると同時に、タイトル、正式状態、担当社員ID、作成日時の保存場所です。managed metadataは、表に存在しないGo Task Domainの永続metadataと、表との対応を検証するdigestを保存します。Taskの復元には両方が必要であり、どちらか一方だけを正しいものと推測しません。

### 既存5列を維持する理由

既存5列はObsidian上の人間向け運用表示であり、Python `ProjectManager`、Review／Revision経路、および既存データが前提としています。列と順序を維持すれば、Python legacy parserはmanaged HTML commentを通常のMarkdown本文として無視し、Task一覧を従来どおり読めます。これはGo移行中のread compatibilityを保ち、PromptやProvider移行とは無関係なPython改修を増やしません。

### sidecar JSONを採用しない理由

sidecar JSONでは、`Tasks.md`の状態更新とJSONのVersion／reason更新が2ファイルに分かれます。通常のfilesystem renameでは2ファイルを1 transactionとして置換できないため、crashやpartial failureで片方だけが新しくなる状態を避けられません。recovery journalやtransaction protocolを追加する案はv0.3の最小TaskStoreとして過剰であり、単一ファイル置換の明確なcommit pointを失います。そのため永続metadata用sidecarは採用しません。

なお、複数Go process間のCASを直列化するためのhidden lock fileは運用上のlockであり、永続状態を分割するsidecar metadataではありません。

### Tasks.mdへの列追加を移行期間中は採用しない理由

Version、failure reason、hold reasonを表の列として追加すると、人間向け表が内部制御情報で肥大化し、固定5列を前提とするPython parser／writerと既存fixtureを破壊します。Pythonをその新形式へ対応させることはlegacyへの再投資にもなります。このためPython製品経路が残る移行期間中は列を追加しません。

### 整合性と拒否規則

TaskStoreは通常の`Create`、`Get`、`Update`で次を検出した場合、推測、自動補完、自動修復を行わず安全に拒否します。

- metadata blockが存在しない
- markerまたはJSONが破損している、未対応versionである
- metadata block、JSON key、表のTask IDが重複している
- 表とmetadataでTask ID集合が異なる
- 表の意味内容と`table_digest`が一致しない
- statusとhold reasonなどDomain不変条件が一致しない

既存のmetadataなし`Tasks.md`をGo管理形式へ変換する処理は、通常TaskStore操作とは分離した明示的migrationとします。migrationは書込みなしのplanで対象Taskと元ファイルrevisionを提示し、明示承認付きapplyで同じrevisionだけを置換します。移行時点を各TaskのVersion 1という新しいCAS baselineとして明示的に確立し、failure reasonは未記録として空にします。5列表からhold reasonを復元できない`保留`Taskが1件でもある場合は推測せずmigrationを拒否します。初回の通常読込でVersion 1とみなす暗黙migrationは行いません。

### Version/CASとreason

- 各Taskの`Version`をmetadataへ永続化します。
- `Create`はVersion 1だけを受け付けます。
- `Update`はlock取得後にディスク上の最新版を再読込し、保存済みVersionが`expectedVersion`、次のVersionが`expectedVersion + 1`の場合だけ置換します。
- stale versionは`task.ErrVersionConflict`として拒否します。
- `LastFailureReason`は失敗した実行事実としてmetadataへ保存し、後続のHoldやCompleteで自動消去しません。
- `HoldReason`は`保留`状態のTaskだけに必須とし、それ以外の状態では空でなければ拒否します。Resume等でDomainが空にした値をそのまま保存します。

複数Go process間のread/compare/writeは、同じProject directoryのstable lock fileに対するOS file lockで直列化します。process内mutexだけを永続CASの根拠にはしません。lockを尊重しないlegacy writerとの同時書込みはサポートせず、後続読込でdigest不一致を検出して拒否します。

### metadata blockのversioning

markerとJSON payloadの両方をversion付きにします。v1 readerはversion不一致、未知field、重複keyを拒否します。将来schemaを変更する場合は、新marker／schema version、明示migration、rollback手順、fixtureを用意します。v1 blockを新形式として暗黙解釈しません。

### atomic write、rollback、crash semantics

更新後の`Tasks.md`全体を同じdirectoryの一時ファイルへ書き、permission設定、file `fsync`、closeを完了してからrenameで原子的に置換し、最後にdirectoryを`fsync`します。

- rename前の失敗では元ファイルを変更せず、一時ファイルを削除します。これがrollbackです。
- renameがcommit pointです。rename後は旧内容へ推測で巻き戻しません。
- rename後のdirectory `fsync`失敗は、置換が見えている可能性のあるpartial failureです。成功扱いせず、`committed=true`を持つ型付きwrite errorとして返します。
- crashがrename前なら旧ファイル、rename後なら新ファイルのいずれかが見え、不完全なファイル内容は公開しません。ただしfilesystem／hardwareが保証する範囲を超えたdurabilityは主張しません。

Task更新とEvent publishは引き続き別commitです。Eventのdurable atomicityはADR-0005どおり将来のTransactional Outboxの対象であり、このTaskStoreへ混在させません。

### Python互換性と将来変更

Python legacy parserは5列表だけを走査するため、末尾のHTML commentを無視してread compatibilityを維持します。一方、managed metadata導入後のPython writerとの同時利用は許可しません。Python writerが表だけを変更した場合、Go TaskStoreはdigest不一致として拒否します。

Python削除後は、5列表を維持する必要性を再評価できます。SQLite等の別TaskStore、表の列変更、Markdownをprojectionへ縮小する案へ移行しても、`task.Store` portとTask Domainは変更しない設計を維持します。形式変更時は新schema、明示migration、互換fixtureを用意します。

## Consequences

- 1ファイルのatomic replacementで、人間向け表示とGo Domain metadataを同じcommit pointへ置けます。
- Python legacy parserは新しいPythonビジネスルールなしで既存5列を読めます。
- metadata欠落やlegacy writerによる表だけの変更は、自動修復されず運用上のmigration／cutover判断が必要になります。
- hidden lock fileとOS file lockにより、同じfilesystem上の協調するGo process間でCASを維持できます。
- directory `fsync`後まで成功を返さないため、commit済みかもしれないpartial failureを呼び出し側が観測できます。
- Task更新とEvent永続化のatomicityは提供しません。durable Outboxはv0.4の別責務です。
