# ADR-0070: Local Data Folder Default and Optional Storage/Viewers

## Status

Accepted

## Context

PHASE PB-2.22からPB-2.24でREADME英日版のstorage positioningを、通常のローカルデータフォルダを標準とし、iCloud Drive／Obsidianを完全に任意とする表現へ揃えました。しかしPB-2.28のCodex監査で、`SECURITY.md`、`docs/ReleaseNotes.md`、`docs/PublicBetaQuickstart.md`、`docs/PublicReleaseChecklist.md`、`docs/SystemOverview.md`、macOS native folder picker（`go/internal/adapter/localos/integration_darwin.go`）、Web UI（`go/internal/httpapi/web/app.js`）にはこの方針とのズレが残っていることが判明しました。具体的には、native pickerがiCloud Driveを開始位置・推奨表示にしていること（ADR-0038）、一般向けcopyが「Vault」という語を保存先の必須条件のように使い続けていること、Settingsのstorage cardとFirst-run Setupの表示がObsidian前提に読める箇所があることです。

技術層（`--vault`、Vault Adapter、Vault schema、managed metadata marker、JSON Contract、folder validation、single-writer、CAS、atomic replacement）は正しく機能しており、変更の必要はありません。ズレがあるのは一般利用者向けのcopyと、native pickerの開始位置・prompt文言だけです。

## Decision

1. 通常のローカル保存場所を、Public Betaのfirst-run一般説明における標準とします。
2. iCloud Driveは任意の保存先として説明します。標準経路にも推奨経路にもしません。
3. Obsidianは任意のviewerとして説明します。WorkCairnの構成要素や必須dependencyとして描写しません。
4. 一般UI・一般文書では保存先を「WorkCairnデータフォルダ」と表示・説明します。
5. macOS native folder pickerは、iCloud Driveを既定の開始位置にも推奨表示にもしません。開始位置は`~/Documents`が存在すればそこ、存在しなければhomeへのfallbackとします。promptはiCloud Driveを推奨しない中立な文言にします。
6. 現行のfolder selection（利用者の明示選択）、validation（`ValidateWorkspaceRoot`）、選択済みrootへの業務データ本体の永続化は維持します。Application Supportのlocal configには選択pathの参照だけをatomicに保存し、業務データ本体はApplication Supportへ保存しません。Application Supportを業務データStoreとして扱う変更ではありません。
7. `--vault` CLI option、Vault Adapter、Vault schema、managed metadata marker、JSON Contractはいずれも変更しません。
8. home directory、filesystem root、iCloud Drive root、WorkCairn markerのない非空directoryを拒否するvalidationは変更しません。
9. single-writer制約、CAS（compare-and-set）、atomic replacementなど既存の安全境界は変更しません。
10. SQLite導入、Application Supportへの業務データ移行、Vault export adapter、その他のstorage migrationは本ADRの対象外とし、将来のM-0 Internal Storage Architectureで別途設計します。
11. ADR-0038の他の決定（native picker、explicit selection、Application Support local config、Keychain経由のClaude connection、trusted LANへpath／secretを渡さない境界など）はすべて維持します。本ADRがsupersedeするのはADR-0038の「pickerはiCloud Driveを推奨開始位置とします」という一文の現在状態記述だけです。ADR-0037／ADR-0038の他の判断はsupersedeしません。

## Consequences

- 一般利用者は、iCloud Driveを特別に用意・推奨されることなく、Mac上の通常の場所へ空のWorkCairn専用データフォルダを作成するだけでFirst-runを完了できます。
- iCloud Driveを使いたい利用者は引き続き自由に選択できます。pickerが積極的に妨げることはありません。
- Obsidianは今までどおり同じフォルダを閲覧する任意手段のままで、機能の追加・削除はありません。
- native pickerの開始位置とpromptの変更は、`ValidateWorkspaceRoot`によるvalidation結果や、既存のcancel／error処理を変えません。
- 一般向けcopyの変更は、Command Ledger、JSON Contract、canonical `storage_kind`、setup Command、Approval境界のいずれにも影響しません。
- SQLite、Vault optional adapter、業務データのApplication Support移行は今回実装せず、必要になった時点でM-0として別ADRを起こします。
- ADR-0037とADR-0038は歴史的記述として変更せず、本ADRが「iCloud推奨開始位置」の現在状態記述だけを更新します。
