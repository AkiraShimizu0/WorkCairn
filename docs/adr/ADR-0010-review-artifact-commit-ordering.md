# ADR-0010: Review JSONをhuman-readable Markdownより先にcommitする

## Status

Accepted

## Context

Python ReviewerWorkerは、Provider出力から検証済みReview JSONと人間向けMarkdownを生成し、`Reviews/<Task ID>.review[.vN].json`と`.md`の2ファイルへ保存します。Revisionは構造化JSONを入力にする一方、人間はMarkdownを確認します。Go移行後も既存ファイル名と内容の互換性を維持する必要がありますが、通常のfilesystem操作では2ファイルを1 transactionとしてcommitできません。

両ファイルの保存途中でprocess停止やI/O失敗が起きた場合、どちらを確定証跡とするかが不明だと、retry時に既存ファイルを採用・削除・上書きする推測が必要になります。これはReview証跡を失う危険があり、v0.3でrecovery journalやCommand Ledgerを先取りすることにもなります。

## Decision

### Canonical evidenceとprojection

構造化JSONをReviewのimmutable canonical evidenceとし、先にcommitします。JSONは検証済みの`verdict`と正規化済み`issues`を保持し、Revision判断の機械可読な入力です。

MarkdownはJSON commit後に作成するhuman-readable projectionです。既存Pythonと同じfront matter、本文、判定、`result_file`参照を維持しますが、Reviewの機械的な正本ではありません。

```text
ReviewService.Execute
→ ReviewStore.Save canonical JSON
→ ReviewStore.Save Markdown projection
```

ReviewStoreはReview Prompt、Provider、Task状態、承認、Audit、Revision作成を知りません。ReviewServiceもVault pathやMarkdown保存を知りません。

### Immutable createとatomic publication

両ファイルは同じ`Reviews` directoryの一時ファイルへ書き、permission設定、file sync、closeの後にhard linkで未存在pathへ公開し、directory syncまで完了して成功とします。

- 既存pathは上書きしません。
- publication前の失敗では一時ファイルだけを除去します。
- publication後のdirectory sync等の失敗は`committed=true`のpartial failureです。
- JSONとMarkdownを自動削除・rollbackしません。

### Partial failure

JSON成功後にMarkdownが失敗した場合、Review保存全体は成功扱いしません。結果と型付きerrorは少なくともcanonical JSONがcommit済みであること、Markdown projectionのcommit有無、失敗stageを示します。

JSONのatomic publication後にSaveが失敗した場合も、visibleなJSONを自動削除しません。Markdown publication後のdirectory sync失敗もpartial failureとして返し、両ファイルが存在すると推測して成功へ変換しません。

### Retryと既存artifact

対象JSONまたはMarkdownのどちらかが既に存在する場合、新規Review保存はpreflightまたはatomic createで拒否します。次を推測しません。

- 既存JSONが今回のProvider出力と同一か
- JSONだけがある状態をMarkdown生成だけで補完してよいか
- Markdownだけがある状態を旧実装の成功として採用してよいか
- 既存artifactを削除、上書き、adoptしてよいか

Review versionは既存Python命名の未指定または`v<数字>`だけを許可し、同じTask IDとversionを同じReview identityとして扱います。

### Python互換性

JSON schema、pretty-printed UTF-8形式、末尾改行、Markdown front matter、ファイル命名は既存Python ReviewerWorkerと互換にします。Python legacy readerとRevisionTaskServiceは保存済みGo Review artifactを読み取れます。ただし通常製品経路ではGo Review processを正本とし、Python ReviewerWorkerをfallbackとして暗黙利用しません。

### 延期する事項

crash recovery、orphan JSONからのMarkdown再生成、reconciliation、automatic retry、idempotency key照合、Review command ledgerはv0.4へ延期します。v0.3ではpartial stateを保存・報告し、明示的な将来recovery commandまたは人間判断なしに修復しません。

## Consequences

- Revisionが依存する構造化判断はhuman projectionより先にimmutable evidenceとして残ります。
- 2ファイルを跨ぐ完全atomicityは提供しませんが、partial stateを隠しません。
- JSONだけが残る状態は安全に観測できる一方、通常retryでは自動修復できません。
- Review Storeのerror/result型とprocess responseはcanonical／projectionのcommit状態を保持する必要があります。
- Review Auditのcommit orderingやdurable Event配送は本ADRの対象外であり、別の明示判断なしにReview Storeへ混在させません。
