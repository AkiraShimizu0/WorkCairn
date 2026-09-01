# Manual macOS Signed Release Procedure

この文書はrelease owner／Human operator向けのrelease engineering手順です。一般利用者向けの[Public Beta Quickstart](PublicBetaQuickstart.md)ではありません。

**現在状態（PHASE PB-3p.2a）**: 初回Public Betaは、未commitのまま複雑性と追加findingが残った完全自動化実装（PB-3o.3 Slice 2 automation attempt）を採用せず、この文書に沿ってHumanが1 stepずつApple標準commandを明示実行するmanual signed release pathで行います。**manualであることは、いかなる検証も省略してよいという意味ではありません。** Developer ID Application署名、Hardened Runtime、secure timestamp、Team ID一致、signed DMG、Apple notarization、staple、Gatekeeper検証、exact content確認、checksumという安全要件は、自動化の有無にかかわらずすべて必須です。完全自動化は将来の`M-RELEASE-1`Checkpointへ延期しています（[ROADMAP.md](ROADMAP.md)参照）。本版は、独立review（PB-3p.2）が報告したcommand実行可能性・placeholder曖昧性・staging／final分離・device identifier取得・検証項目欠落に関するfindingを、focused correction（PB-3p.2a）としてすべて反映したものです。方針・安全要件自体は変更していません。

各stepは、直前のstepの結果をHumanが確認してから次のstepへ進む前提で書かれています。まとめて1つのscriptとして貼り付けて一括実行しないでください。

## 1. Scope

このprocedureが対象とするのは、macOS／arm64向けPublic Beta candidateの署名・DMG化・notarization・staple・検証・promotionだけです。次は対象外です。

- Apple Developer Programへの新規加入手続き自体（Apple公式portalでHumanが行う）
- Developer ID証明書の発行申請自体（Apple公式手順でHumanが行う）
- `notarytool` Keychain profileの作成自体（`store-credentials`はHumanが対象build host上で直接実行する、repository外・本procedure外の一度きりの作業）
- tag push後のGitHub Release本文執筆（[Release Notes](ReleaseNotes.md)を下敷きにHumanが作成する）
- Provider（Anthropic Claude）Acceptance自体の実施内容（[PublicBetaFirstRunAcceptance.md](PublicBetaFirstRunAcceptance.md)を参照）

このprocedureはWorkCairnのproduct runtime／Coreへrelease engineering機能を追加しません。initial Public Beta artifactは、repository外でこのbounded manual procedureに沿ってHumanが生成します。将来のautomation（`M-RELEASE-1`）は、製品architectureとは別のoptionalなrelease-engineering layerとして扱います。このmanual procedureの存在を、release automationが製品として実装済みであることの根拠にしないでください。

## 2. Preconditions

次がすべて確認できるまで、このprocedureを開始しないでください。

- [ ] repositoryのworking treeがcleanで、`HEAD`がrelease候補として確定したcommitと一致する。
- [ ] `go/internal/releaseinspector`／`go/cmd/workcairn-release-inspector`（[Architecture.md](Architecture.md)参照）はsource-onlyのrelease-engineering primitiveであり、正式3 binaryではなく、archive／DMGへ含まれず、production signing workflowへ未接続であることを理解している。このprocedure自体もWorkCairnのGo sourceを実行しない——すべてApple公式CLI（`security`、`codesign`、`hdiutil`、`xcrun notarytool`、`xcrun stapler`、`spctl`）とGo toolchainの直接実行です。
- [ ] `scripts/lib/release_tools.sh`はtool boundary（fixed absolute path bundle）だけを提供し、workflow自体を実行しないことを理解している。
- [ ] Apple Developer Program加入、Developer ID Application証明書、`notarytool` Keychain profileという3つのHuman prerequisiteをこれから確認する準備ができている（§7）。
- [ ] 十分な時間（notarizationのclient-side wait上限は最大90分）と、途中で予期しない認証画面が出た場合に安全に中断できる状況を確保している。
- [ ] `<STAGING_DIR>`として使うfilesystem上に十分な空き容量がある。
- [ ] `<STAGING_DIR>`はこの試行専用に新規作成する（既存directoryを再利用しない）。開始時点で`<STAGED_DMG_PATH>`が存在しないことを確認する。
- [ ] `<FINAL_DIR>`、`<FINAL_DMG_PATH>`、対応するchecksumのいずれも、開始時点で存在しないことを確認する（既存の確定済みcandidateを上書きしない）。

## 3. Four independent Human authorizations

次の4つは、それぞれ**別のrelease owner明示承認**を必要とする独立したstepです。1回の承認が複数を兼ねることはありません。

1. **final main push** — release候補commitをGitHub `main`へpushする前の承認。
2. **local annotated tag creation** — `git tag -a`実行前の承認（§8）。
3. **tag push** — Human Acceptance完了後、tagをremoteへpushする前の承認（§25）。
4. **GitHub Release creation** — tag push後、GitHub Release作成前の承認（§26）。

tag pushとGitHub Release作成は、Human Acceptance（§24）完了前には行いません。

## 4. Credential-domain separation

このprocedureは3つの独立したcredential domainに触れます。互いに混同・共有しないでください。

| Domain | 用途 | 保存先 |
|---|---|---|
| Apple | Developer ID証明書、`notarytool` profile | HumanのmacOS login Keychain（build host） |
| GitHub | main push、tag push、Release作成 | Humanの既存git／GitHub credential helper |
| Anthropic（Provider） | signed build上でのProvider Acceptance（[PublicBetaFirstRunAcceptance.md](PublicBetaFirstRunAcceptance.md) 該当section） | WorkCairn自身のKeychain item（`com.workcairn.provider.anthropic`）、本procedureとは別途 |

**AIエージェント（Claude Code等）は、上記いずれのcredentialも受け取りません。** private key material、Apple ID、app-specific password、API keyをAIとのchatへ貼り付けないでください。

## 5. Authentication prompt warning

以下は、このprocedure中に実際に表示される可能性がある認証画面です。事前に把握しておいてください。

- `codesign ... --timestamp`実行時、署名private keyへのKeychainアクセス許可、またはmacOSのpassword／Touch ID promptが表示される場合があります。
- `xcrun notarytool submit ...`実行時、指定した`--keychain-profile`に保存された参照を使ってApple notary serviceへ通信します。profile自体の作成時（本procedure外）にApple IDでの認証が必要です。
- Apple Developer Program portal（`developer.apple.com`）での証明書取得作業自体に、Apple IDのlogin・2要素認証が必要です。
- `git push`実行時、SSH鍵のpassphraseまたはGitHub credential helperのpromptが表示される場合があります。
- Provider Acceptance実施時は、Anthropic credentialに関するmacOS native hidden-input promptが表示されます（Apple／GitHub credentialとは別domain）。

**予期しない認証画面が表示された場合は、何も入力せず、キャンセルし、その時点で停止し、どのcommandを実行した直後に表示されたかだけを記録してください。** 見慣れた画面であっても、このprocedureが明示的に要求していない認証要求には応じないでください。

## 6. Fixed candidate identity

このprocedureを通じて、次の値を最初に確定し、以降のすべてのstepで同じ値を使います（値そのものはこの文書に書かず、Human operatorが自分の環境で管理してください）。`<TAG>`と`VERSION`ファイルの内容は**先頭の`v`を含めて完全に同一の文字列**です（例: いずれも`v1.0.0-beta.1`）。`v`の有無を変換・除去する箇所はこのprocedureのどこにもありません。

- `<SOURCE_COMMIT>` — release候補のfull commit SHA（tag対象commitと一致させる）。
- `<TAG>` — 例: `v1.0.0-beta.1`（先頭`v`込みの完全な文字列。`VERSION`ファイルの内容、`buildinfo.Version`へ埋め込む値と、いずれも同じこの文字列を使う）。
- `<SOURCE_ROOT>` — cleanであることを確認済みで、`HEAD`が`<SOURCE_COMMIT>`と一致し、local annotated`<TAG>`のtargetも`<SOURCE_COMMIT>`と一致する、frozen tag-target checkoutのrepository root。build（§9）、expected manifest生成（§9）、byte-for-byte照合（§12、§19）はすべてこの同一checkoutを参照する。username・absolute pathそのものはdurable evidenceへ記録しない。
- `<BUILD_DATE>` — canonical UTC RFC3339形式（例: `2026-09-01T00:00:00Z`）。
- `<IDENTITY_SHA1>` — Developer ID Application証明書のSHA-1 fingerprint（40桁hex、大文字小文字は正規化して統一する）。
- `<TEAM_ID>` — Apple Developer ProgramのTeam ID。
- `<NOTARY_PROFILE>` — `notarytool` Keychain profileの名前（secretではない参照）。
- `<DIST_ROOT>` — このprocedure専用の base directory。`<STAGING_DIR>`と`<FINAL_DIR>`は必ずこの同一filesystem上に置く（cross-filesystem moveによる不完全な結果を避けるため）。
- `<STAGING_DIR>` — `<DIST_ROOT>`配下の、この試行専用の使い捨てdirectory（fresh・collision-safeな名前）。署名・DMG作成・notarization・staple・mount検証・checksum生成まで、確定前のすべての中間作業をここで行う。
- `<FINAL_DIR>` — `<DIST_ROOT>`配下の、確定済みcandidateだけを置く最終directory。promotion（§23）で初めて作成し、それまでは存在しない。
- `<PACKAGE_ROOT>` — package root directory名。`workcairn_<TAG>_darwin_arm64`（DMGのvolume nameと同一文字列）。build出力は直接`<STAGING_DIR>/<PACKAGE_ROOT>/`配下へ書く。
- `<DMG_BASENAME>` — `workcairn_<TAG>_darwin_arm64.dmg`。
- `<STAGED_DMG_PATH>` — `<STAGING_DIR>/<DMG_BASENAME>`（署名・notarize・staple・検証はすべてこのpathに対して行う）。
- `<FINAL_DMG_PATH>` — `<FINAL_DIR>/<DMG_BASENAME>`（promotion後の確定path）。
- `<CHECKSUM_BASENAME>` — `<DMG_BASENAME>.sha256`。
- `<MOUNT_POINT>` — `<STAGING_DIR>/mnt`（この試行専用のfresh mountpoint）。
- `<DEVICE_IDENTIFIER>` — mount時（§17）に取得する値。事前に固定せず、`hdiutil attach`の結果から都度取得する。
- `<SUBMISSION_ID>` — §15の最初の`notarytool submit`結果から取得した、同一submissionのID。timeout発生時にIDが得られた場合だけ設定する（新しいsubmissionを作るための値ではない）。raw JSON全体ではなく、sanitized fieldとしてのみ記録してよい。

canonical code-signing identifier（固定、変更しない）:

- `com.workcairn.cli.workcairn-core`
- `com.workcairn.cli.workcairn`
- `com.workcairn.cli.workcairn-daemon`
- `com.workcairn.dist.macos`（DMG container自体）

canonical signing order（固定、変更しない）:

1. `workcairn-core`
2. `workcairn`
3. `workcairn-daemon`
4. DMG

## 7. Apple prerequisites

次をHumanが確認し、記録します（値そのものはrepository docsへ書きません）。

1. Apple Developer Program（またはEnterprise Program）への加入とAccount Holder権限を確認する。
2. Developer ID Application証明書をApple公式手順で取得し、build host macOS Keychainへ保持する。
3. 取得した証明書のSHA-1 fingerprintを確認し、`<IDENTITY_SHA1>`として記録する。
4. `security find-identity -v -p codesigning`を実行し、`<IDENTITY_SHA1>`に一致する行が**exactly 1件**であることを確認する。0件・複数件はいずれもfail-closedで停止する（別のidentityへの自動切替はしない）。
5. 証明書の有効期限を確認する。期限切れの場合はこのprocedureを進めない。
6. 実Team IDを確認し、`<TEAM_ID>`として記録する。
7. `xcrun notarytool store-credentials`を対象build host上で直接実行し、`<NOTARY_PROFILE>`という名前のprofileを作成する（このprocedure・AIエージェントのいずれもこのcommandを実行しません）。profile作成時に使用したTeam IDが`<TEAM_ID>`と一致することを記録する。
8. `xcrun notarytool submit --help`相当の出力から、`--wait`と`--timeout`の両方が存在することを確認する。いずれか欠落時はfail-closedで停止し、GNU `timeout`やGo supervisorへのfallbackはしない。

これらすべてが確認できるまで§8以降へ進まないでください。

## 8. Local annotated tag

**Human authorization #2（local annotated tag creation）をここで得てください。**

1. working treeがcleanで、`HEAD`が`<SOURCE_COMMIT>`と一致することを確認する。
2. `VERSION`ファイルの内容が`<TAG>`と**完全一致**（先頭の`v`を含め、1文字も変換せずそのまま一致）することを確認する。`VERSION`は既に`v`から始まる文字列（例: `v1.0.0-beta.1`）であり、`<TAG>`もこのprocedure全体を通じて同じ`v`込みの文字列を使う。どちらか一方だけ`v`を除いた形と比較しない。
3. 承認を得た上で、local annotated tagを作成する。
   ```bash
   git tag -a <TAG> -m "<TAG>"
   ```
4. 実行後、tag nameとtag target commitが`<SOURCE_COMMIT>`と一致することを確認する。
   ```bash
   git rev-list -n 1 <TAG>
   ```
5. **この時点でtag pushは行いません。** remote公開はHuman Acceptance完了後の別の承認（§25）です。

## 9. Build from tag target

build直前に、次をすべて再確認します（§8で確認済みであっても、時間が経過している可能性があるため再確認します）。

- [ ] working treeがcleanである。
- [ ] `git rev-parse HEAD`が`<SOURCE_COMMIT>`と一致する。
- [ ] `git rev-list -n 1 <TAG>`が`<SOURCE_COMMIT>`と一致する（local annotated tagのtargetがずれていない）。
- [ ] `VERSION`ファイルの内容が`<TAG>`と完全一致する。
- [ ] source・docsが、Acceptance開始前のfrozen candidateのままであり（§8以降にsourceへ変更を加えていない）、今から署名するbuildと以降のAcceptanceが同一commitに対するものである。

上記がすべて確認できたら、同じ`<SOURCE_ROOT>`から、package rootのexpected manifest（sorted、relative path、entry type付き）を生成します。`scripts/verify-release-archive.sh`の既存allow-list（`bin/`、3 binary、`VERSION`、`LICENSE`、`README.md`、`CHANGELOG.md`、`SECURITY.md`、`CONTRIBUTING.md`、`docs/`配下の`*.md`／`*.mmd`、`docs/adr/`配下の`*.md`）と一致させます。3 binaryはbuild出力であり`<SOURCE_ROOT>`上には存在しないためfixedなentryとして列挙し、docs配下は実際に`<SOURCE_ROOT>`をlistingして導出します（存在しないfileを推測で列挙しない）。

```bash
(
  cd <SOURCE_ROOT>
  {
    printf '%s\n' "bin/" "bin/workcairn-core" "bin/workcairn" "bin/workcairn-daemon" \
      "VERSION" "LICENSE" "README.md" "CHANGELOG.md" "SECURITY.md" "CONTRIBUTING.md" "docs/" "docs/adr/"
    for f in docs/*.md docs/*.mmd; do [ -e "$f" ] && printf '%s\n' "$f"; done
    for f in docs/adr/*.md; do [ -e "$f" ] && printf '%s\n' "$f"; done
  } | LC_ALL=C sort > <STAGING_DIR>/expected-manifest.txt
)
```

この`<STAGING_DIR>/expected-manifest.txt`を、staged package root（§12）とmounted volume root（§19）の両方の照合基準として使います。expected manifestにはrelative pathだけを記録し、username・absolute path・credentialのいずれも含めません。full automationの実装ではなく、Humanがcommand結果を比較するmanual procedureのままです。

続けて、`<TAG>`対象commitから、native macOS build hostで3 binaryをbuildします。CGO有効。build出力は`<STAGING_DIR>/<PACKAGE_ROOT>/bin/`へ直接書き込みます（後続のDMG化でこのdirectoryをそのままpackage rootとして使うため、別directoryへの移動stepを挟みません）。

```bash
cd <SOURCE_ROOT>/go
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath \
  -ldflags "-s -w \
    -X github.com/AkiraShimizu0/WorkCairn/go/internal/buildinfo.Version=<TAG> \
    -X github.com/AkiraShimizu0/WorkCairn/go/internal/buildinfo.Commit=<SOURCE_COMMIT> \
    -X github.com/AkiraShimizu0/WorkCairn/go/internal/buildinfo.BuildDate=<BUILD_DATE>" \
  -o <STAGING_DIR>/<PACKAGE_ROOT>/bin/workcairn-core ./cmd/workcairn-core
```

同様に`workcairn`、`workcairn-daemon`をbuildします（canonical signing orderと同じ順序で行うと後続stepと対応が付けやすい）。3つの`-X`はいずれも省略せず、`github.com/AkiraShimizu0/WorkCairn/go/internal/buildinfo`というfull package pathをそのまま3回とも記載します（`...`のような省略記号は使いません——Go linkerの`-X`は毎回full import pathを要求し、省略記号は無効な引数になります）。

build後、各binaryを実行し、`version`／`--version`出力のversion／commit／build dateが`<TAG>`／`<SOURCE_COMMIT>`／`<BUILD_DATE>`と一致することを確認します。

## 10. Sign 3 binaries

canonical signing order（core → cli → daemon）で、1 binaryずつ署名します。

1. `workcairn-core`を署名する。
   ```bash
   codesign --sign <IDENTITY_SHA1> --identifier com.workcairn.cli.workcairn-core --options runtime --timestamp <STAGING_DIR>/<PACKAGE_ROOT>/bin/workcairn-core
   ```
2. 実行結果（成功／failure／認証prompt）を確認してから次へ進む。
3. `workcairn`を署名する。
   ```bash
   codesign --sign <IDENTITY_SHA1> --identifier com.workcairn.cli.workcairn --options runtime --timestamp <STAGING_DIR>/<PACKAGE_ROOT>/bin/workcairn
   ```
4. `workcairn-daemon`を署名する。
   ```bash
   codesign --sign <IDENTITY_SHA1> --identifier com.workcairn.cli.workcairn-daemon --options runtime --timestamp <STAGING_DIR>/<PACKAGE_ROOT>/bin/workcairn-daemon
   ```

## 11. Verify 3 binaries

各binaryごとに、署名・requirement・Team ID・identifier・Hardened Runtime・secure timestamp・entitlementsをまとめて確認します。`--deep`は使いません——3 binaryはbundle構造を持たない単一実行ファイルであり、nested codeを持たないため、deep traversalは不要かつAppleの現行guidanceでも非推奨です。

```bash
codesign --verify --strict -R="anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.13] /* exists */ and certificate 1[field.1.2.840.113635.100.6.2.6] /* exists */ and certificate leaf[subject.OU] = \"<TEAM_ID>\"" <STAGING_DIR>/<PACKAGE_ROOT>/bin/workcairn-core
codesign -dvv <STAGING_DIR>/<PACKAGE_ROOT>/bin/workcairn-core
codesign -d --entitlements :- <STAGING_DIR>/<PACKAGE_ROOT>/bin/workcairn-core
```

**`codesign -dvv`の出力はstderrへ書かれます。** stdoutが空であっても失敗ではありません——terminalでそのまま実行すればstderrも通常表示されますが、出力をfileへ記録する場合は`2>&1`等でstderrを明示的に捕捉してください。空のstdoutだけを見て成功・失敗を判定しないでください。

`-dvv`出力から次を目視確認します。

- `Identifier=`が該当binaryのcanonical identifierと完全一致する。
- `TeamIdentifier=`が`<TEAM_ID>`と完全一致する。
- `flags=`に`runtime`が含まれる（Hardened Runtimeが有効）。
- `Timestamp=`が具体的な日時を示す値であり、`Timestamp=none`ではない（secure timestampが付与されている）。

`codesign -d --entitlements :-`の出力（stdout）が**空である**ことを確認します。ADR-0071「4」のRelease entitlements allow-listは現時点で空集合であり、何らかのentitlement plistが出力された場合は想定外のentitlementとして扱い、停止します。

3 binaryすべてで実施してから次へ進みます。

## 12. Assemble staged package root

`<STAGING_DIR>/<PACKAGE_ROOT>/bin/`には、署名済みの3 binaryだけが既に存在します（§9でbuild、§10-11で署名・検証済み）。次のfileを`<STAGING_DIR>/<PACKAGE_ROOT>/`直下・`docs/`配下へ追加し、package rootを完成させます。

- `VERSION`、`LICENSE`、`README.md`、`CHANGELOG.md`、`SECURITY.md`、`CONTRIBUTING.md`
- `docs/`（`docs/adr/`を含む）

これらのfileはすべて、§9で確認した`<SOURCE_ROOT>`からcopyします（他のsourceやbackupからcopyしない）。

続けて、staged package rootの実際のmanifestを生成し、§9で生成した`<STAGING_DIR>/expected-manifest.txt`と完全一致することを確認します。GNU find固有の`-printf`は使わず、macOS標準のBSD `find`／`sed`／`sort`だけで構成します。

```bash
(
  cd <STAGING_DIR>/<PACKAGE_ROOT>
  {
    find . -mindepth 1 -type d -print | sed 's#^\./##; s#$#/#'
    find . -mindepth 1 -type f -print | sed 's#^\./##'
  } | LC_ALL=C sort > <STAGING_DIR>/actual-manifest-staged.txt
)
diff <STAGING_DIR>/expected-manifest.txt <STAGING_DIR>/actual-manifest-staged.txt
```

差分が1件でもあれば（不足・追加・type mismatchのいずれも）停止します。symlink・hardlinkが見つかった場合も停止します（`find <STAGING_DIR>/<PACKAGE_ROOT> -type l`相当で別途確認）。

`docs/`配下の各`*.md`／`*.mmd`、`docs/adr/`配下の各`*.md`、およびroot直下の`VERSION`／`LICENSE`／`README.md`／`CHANGELOG.md`／`SECURITY.md`／`CONTRIBUTING.md`は、`<SOURCE_ROOT>`上の対応fileと`diff -q`等でbyte-for-byte一致することを1件ずつ確認します（3 binaryはbuild出力のためこの一致確認の対象外）。

完成後、次も確認します。

- [ ] `bin/`直下は`workcairn-core`、`workcairn`、`workcairn-daemon`の3つだけである。
- [ ] 上記の承認済み配布文書だけが存在し、それ以外のfileが混入していない。
- [ ] signing用の一時file、log、sidecar file（`.cstemp`等）が残っていない。
- [ ] symlink、hardlinkが1つも存在しない。
- [ ] `<STAGED_DMG_PATH>`がまだ存在しない（新規作成であることを再確認）。

## 13. Create staged DMG

UDIF圧縮read-only（UDZO）形式でDMGを作成します。既存fileの上書きを避けるため`-ov`は使いません——`<STAGED_DMG_PATH>`が既に存在する場合、次のcommandはfailします。failした場合は既存fileの由来を確認してから対応してください（無条件で上書き・削除しない）。

`-srcfolder <STAGING_DIR>/<PACKAGE_ROOT>`を使うため、`<PACKAGE_ROOT>`というdirectory自体ではなく、その直下の内容（`bin/`、`VERSION`等）がmounted volumeのrootへ配置されます。したがって§17以降でmount後を参照するpathは`<MOUNT_POINT>/<PACKAGE_ROOT>/...`ではなく`<MOUNT_POINT>/...`になります。

```bash
hdiutil create -srcfolder <STAGING_DIR>/<PACKAGE_ROOT> -volname <PACKAGE_ROOT> -fs HFS+ -format UDZO <STAGED_DMG_PATH>
```

作成成功と、DMGが単一volume・単一package rootであることを確認します。

## 14. Sign staged DMG

DMG container自体を、3 binaryと同じDeveloper ID Application identityで、別のcanonical identifier（`com.workcairn.dist.macos`）で署名します。

```bash
codesign --sign <IDENTITY_SHA1> --identifier com.workcairn.dist.macos --options runtime --timestamp <STAGED_DMG_PATH>
```

署名後、次を確認します（§11と同様、`--deep`は使いません）。

```bash
codesign --verify --strict -R="anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.13] /* exists */ and certificate 1[field.1.2.840.113635.100.6.2.6] /* exists */ and certificate leaf[subject.OU] = \"<TEAM_ID>\"" <STAGED_DMG_PATH>
codesign -dvv <STAGED_DMG_PATH>
```

- signatureが有効である（`--verify --strict`が成功する）。
- `Identifier=`が`com.workcairn.dist.macos`と完全一致する。
- `TeamIdentifier=`が`<TEAM_ID>`と完全一致する。
- `Timestamp=`が具体的な日時を示す値である。

**この検証はcodesignレベルの署名確認だけです。** notarization（§15）とstaple／staple validate（§16）は別の検査であり、このstepの成功をもってnotarization・stapleが完了したとはみなしません。

## 15. Notarize staged DMG

**canonical submit invocation（固定、これ以外の形は使わない）：** commandの生stdout／stderrをterminalへそのまま出さず、`umask 077`のsubshell内でrestrictedな一時fileへredirectします。

```bash
(
  umask 077
  xcrun notarytool submit <STAGED_DMG_PATH> \
    --keychain-profile <NOTARY_PROFILE> \
    --wait \
    --timeout 90m \
    --output-format json \
    > "<STAGING_DIR>/notary-submit.json" \
    2> "<STAGING_DIR>/notary-submit.stderr"
)
```

`--output-format json`は、後述のsanitized evidence抽出をHumanが目視で正確に行うために付与します（`notarytool`のcanonical duration契約自体はADR-0071「7」の`--wait --timeout 90m`のまま変更していません）。

- commandのexit statusをそのまま確認します。
- raw stdout（`<STAGING_DIR>/notary-submit.json`）とraw stderr（`<STAGING_DIR>/notary-submit.stderr`）は、いずれも`<STAGING_DIR>`内のこの試行専用のrestrictedな一時fileへだけ保存されます（`umask 077`によりowner以外読めません）。repository、chat、GitHub Release本文、durable Final Reportへ、これらのfileの生内容を貼り付けないでください。
- JSONが空、破損（parse不能）、必要field（`status`、`id`）が欠落、型が想定と異なる場合は、状態不明として安全に停止します。
- statusが`Rejected`／`Invalid`／未知の値の場合は停止します。

durable Final Reportへ転記してよいのは、次のsanitized fieldだけです。

- submission ID
- status（`Accepted`／`Invalid`等）
- submitted／completed timestamp
- issue件数
- normalized package-relative issue path、Appleが返す定型的なsafe issue code

credential、profile secret、raw issue body全文、username・home directory等のlocal絶対pathは転記しません。

timeout発生時、submission IDが得られた場合だけ、同じIDに対して次のcommandを**1回だけ**実行してよい。submitと同様`umask 077`のsubshell内で、submitとは別のfile（`notary-info.json`／`notary-info.stderr`）へredirectし、submit時のfileを上書き・混同しません。

```bash
(
  umask 077
  xcrun notarytool info <SUBMISSION_ID> \
    --keychain-profile <NOTARY_PROFILE> \
    --output-format json \
    > "<STAGING_DIR>/notary-info.json" \
    2> "<STAGING_DIR>/notary-info.stderr"
)
```

IDが得られなかった場合は状態確認不能として停止し、re-submitしません。timeout後のこのstatus確認結果が`Accepted`であっても、自動でstapleへ進みません。Human判断を待って停止します。retry、resubmit、別profileへのfallbackはいずれも行いません。

このstepは**Human判定のためのJSON出力**であり、JSON parserの自動化はこのCheckpointでは実装しません。Humanが目視でallow-listされたfieldだけを読み取り、記録します。このCheckpoint自体はこれらのcommandを実行しません——実実行時に認証promptが表示され得ることは§5のとおりです。

statusが`Accepted`であることを確認できた場合だけ、次のstepへ進みます。

## 16. Staple staged DMG

Acceptedを確認した場合だけ実行します。

```bash
xcrun stapler staple <STAGED_DMG_PATH>
xcrun stapler validate <STAGED_DMG_PATH>
```

両方の成功を確認してから次へ進みます。

## 17. Mount staged DMG and obtain device identifier

read-only・no-browseでfresh mountpointへmountし、plist形式の結果を、作成された瞬間からrestrictedなfileへ保存します。

```bash
(
  umask 077
  hdiutil attach \
    -readonly \
    -nobrowse \
    -mountpoint <MOUNT_POINT> \
    -plist \
    <STAGED_DMG_PATH> \
    > "<STAGING_DIR>/attach-result.plist"
)
```

- `umask 077`のsubshell内で実行するため、`<STAGING_DIR>/attach-result.plist`は作成された瞬間からowner以外読めません。
- commandのexit statusを確認してからplistを読みます。exit statusが非ゼロの場合、plistの内容を信頼せず停止します。
- mountが成功し、実際のmount pointが`<MOUNT_POINT>`と一致することを確認する。
- `<STAGING_DIR>/attach-result.plist`を開き、`system-entities`配列の中から`mount-point`が`<MOUNT_POINT>`と**完全一致**するentryをexactly one探す。該当entryが0件、複数件、または`dev-entry`が空値の場合は、いずれもfail-closedで停止する（曖昧な推測で1件を選ばない）。
- そのentryの`dev-entry`の値を、**加工せずそのまま**`<DEVICE_IDENTIFIER>`として記録する。suffixを除去してparent diskを推測したり、別のentryのdeviceへ置き換えたりしない。
- `<DEVICE_IDENTIFIER>`が`/dev/diskN`または`/dev/diskNsM`のような、Apple disk device識別子の想定される形（`/dev/disk`で始まる文字列）であることを確認する。想定と異なる形の場合は停止する。
- 値を記録した後、`<STAGING_DIR>/attach-result.plist`を削除する。この生plist（local絶対pathを含み得る）はdurable Release evidenceへ含めません——記録してよいのは抽出した`<DEVICE_IDENTIFIER>`の値だけです。

## 18. Verify Gatekeeper（DMG層／3 CLI層）

DMG層と内部3 CLI層を分離して検証します。**両方が必須です（どちらか一方では不可）。**

**DMG層**:

```bash
spctl --assess --type open --context context:primary-signature <STAGED_DMG_PATH>
```

**内部3 CLI層**（§17でmount済みの`<MOUNT_POINT>`を使用）:

```bash
spctl --assess --type exec <MOUNT_POINT>/bin/workcairn-core
spctl --assess --type exec <MOUNT_POINT>/bin/workcairn
spctl --assess --type exec <MOUNT_POINT>/bin/workcairn-daemon
```

Gatekeeper rejectが1件でも発生した場合は、known limitationとして容認せず、署名・notarization手順のどこかに誤りがあるものとして扱い、停止します。

## 19. Check exact content

mountされたvolume rootについて、staged package rootと同じ形式のsorted actual manifestを生成し、§9で生成した`<STAGING_DIR>/expected-manifest.txt`と完全一致することを確認します。目視の印象だけで「exact」と判断しないでください。`-srcfolder <STAGING_DIR>/<PACKAGE_ROOT>`でDMGを作成しているため、mounted volume rootは`<PACKAGE_ROOT>`という中間directoryを持たず、`<MOUNT_POINT>`直下に`bin/`等が配置されます。GNU find固有の`-printf`は使わず、§12と同じmacOS標準のBSD `find`／`sed`／`sort`だけで構成します。

```bash
(
  cd <MOUNT_POINT>
  {
    find . -mindepth 1 -type d -print | sed 's#^\./##; s#$#/#'
    find . -mindepth 1 -type f -print | sed 's#^\./##'
  } | LC_ALL=C sort > <STAGING_DIR>/actual-manifest-mounted.txt
)
diff <STAGING_DIR>/expected-manifest.txt <STAGING_DIR>/actual-manifest-mounted.txt
```

差分が1件でもあれば（不足・追加・type mismatchのいずれも）停止します。symlinkが1つも存在しないこと、hardlink（link count > 1）が1つも存在しないことも確認します（`find <MOUNT_POINT> -type l`相当で別途確認）。

`docs/`配下の各`*.md`／`*.mmd`、`docs/adr/`配下の各`*.md`、およびroot直下の承認済み配布文書は、`<SOURCE_ROOT>`上の対応fileと`diff -q`等でbyte-for-byte一致することを確認します（§12で確認済みの場合でも、mountされたcopyに対して改めて確認します）。

## 20. Check architecture and metadata

mountされた各binaryについて、Mach-O 64-bit arm64であることを確認します（例: `file <MOUNT_POINT>/bin/workcairn-core`相当の出力で`Mach-O 64-bit executable arm64`を確認）。3 binaryすべてで実施します。

続けて、各binaryを実際に実行し、version metadataを確認します。

```bash
<MOUNT_POINT>/bin/workcairn version
<MOUNT_POINT>/bin/workcairn-daemon --version
<MOUNT_POINT>/bin/workcairn-core --version
```

出力のversion／commit／build dateが`<TAG>`／`<SOURCE_COMMIT>`／`<BUILD_DATE>`と一致することを確認します。

## 21. Detach

```bash
hdiutil detach <DEVICE_IDENTIFIER>
```

detach成功は、checksum生成（§22）とpromotion（§23）へ進むための必須条件です。detachが失敗した場合、このcandidateをfinalとして扱わず、checksum生成・promotionへ進まず、原因を記録して停止します。detachは正確に1回だけ実行します。

## 22. Generate and verify staged checksum

**stapleされた最終bytesに対して**checksumを生成します（staple前のbytesへ生成しないでください）。checksumファイルへlocal絶対pathやusernameが記録されないよう、`<STAGING_DIR>`へ`cd`したsubshellでbasenameだけを対象に実行します。

```bash
(cd <STAGING_DIR> && shasum -a 256 "<DMG_BASENAME>" > "<CHECKSUM_BASENAME>")
```

生成後、checksumファイルにDMGのbasenameだけが記録されていることを確認し、実際にDMGと一致することを独立して再確認します。

```bash
(cd <STAGING_DIR> && shasum -a 256 -c "<CHECKSUM_BASENAME>")
```

## 23. Promote to final

すべて（署名・DMG作成・notarization・staple・mount検証・Gatekeeper・content確認・metadata確認・detach・checksum生成と検証）が`<STAGING_DIR>`内で成功した後だけ、DMGとchecksumを`<FINAL_DIR>`へ確定させます。

1. `<FINAL_DIR>`が依然として存在しないことを、promotionの直前に**再確認**します（§2の precondition確認から時間が経過しているため、再チェックが必要です）。既に存在する場合は、既存candidateを無条件に上書きせず停止し、原因を確認してください。
2. `<FINAL_DIR>`を`<DIST_ROOT>`配下（`<STAGING_DIR>`と同一filesystem）に新規作成します。
3. `<STAGED_DMG_PATH>`と`<CHECKSUM_BASENAME>`を、basenameを保ったまま`<FINAL_DIR>`へ移動します（`<FINAL_DMG_PATH>`とその隣接checksumになります）。
4. 移動後、`<FINAL_DIR>`内で改めて`shasum -a 256 -c`を実行し、checksumが一致することを再確認します。
5. `<FINAL_DMG_PATH>`とそのchecksumの両方が存在し、basenameが期待どおりであることをHumanが目視で再確認します。

次のルールを守ります。

- DMGとchecksumの一方だけが`<FINAL_DIR>`へ移動され他方が失敗した場合、この`<FINAL_DIR>`はsuccessful candidateとして扱いません。何が移動され何が移動されなかったかを正確に記録し、停止します（自動rollbackは行わず、Human判断を待ちます）。
- 既存の`<FINAL_DIR>`、既存の`<FINAL_DMG_PATH>`、既存のchecksumを無条件に上書きしません。
- checksumとbasenameの検証が完了するまで、`<FINAL_DIR>`内のfileをfinal candidateとして次のstepへ進めません。
- GitHub Release（§26）へ添付するのは、`<FINAL_DIR>`内のこのpairだけです。`<STAGING_DIR>`内の中間成果物を添付しません。

## 24. Human Acceptance

signed DMG（`<FINAL_DMG_PATH>`）に対し、[PublicBetaFirstRunAcceptance.md](PublicBetaFirstRunAcceptance.md)の該当sectionをすべて実施します。§4以降のいずれも、automationの有無ではなく、**signed／notarized／stapled／quarantined DMGが生成されたこと**を条件とします。

- New-user Keychain Acceptance
- Upgrade Keychain Acceptance（build N→N+1）
- Gatekeeper／quarantined download Acceptance
- Provider（Anthropic）Acceptance

Humanの既存real Anthropic credentialは使用しません。test用credentialを使い、Acceptance完了後に失効またはrotationします。Keychain／Provider／Gatekeeperのいずれの条件も、automationが実装されていないことを理由に緩和しません。

## 25. Tag push

**Human authorization #3（tag push）をここで得てください。** Human Acceptance（§24）が完了する前には行いません。

```bash
git push origin <TAG>
```

## 26. GitHub Release

**Human authorization #4（GitHub Release creation）をここで得てください。**

1. GitHub Releaseを`<TAG>`向けに作成する。
2. `<FINAL_DIR>`内の、signed／notarized／stapled DMG（`<FINAL_DMG_PATH>`）と、対応する`.sha256`をasset添付する。`<STAGING_DIR>`内の中間成果物は添付しない。
3. [Release Notes](ReleaseNotes.md)の該当sectionを本文として使用する。
4. canonical asset名（`<DMG_BASENAME>`）とmatchingする`.sha256`（`<CHECKSUM_BASENAME>`）であることを確認する。

## 27. Failure policy

- いずれのstepでもcommand failure時は、自動再実行しません。
- 同じcommandを条件を変えずに繰り返し実行しません（no retry-until-green）。
- 別のidentity、別のprofile、別のTeam IDへの自動fallbackはしません。
- 予期しない認証画面が出た場合は、値を入力せず、キャンセルし、停止し、どのcommand実行直後に表示されたかだけを記録します。
- failureが発生した場合、原因を修正した上で、この文書の該当stepからやり直します（前のstepの結果を再利用したまま先へ進みません）。
- promotion（§23）で片方のfileだけが`<FINAL_DIR>`へ移動された場合は、successful candidateとして扱わず、Human判断を待って停止します。

## 28. Evidence policy

- raw notarization output（`notarytool submit`／`info`の生JSON出力全体）、credential、profile secretを公開文書・chat・repositoryへ保存しません。保存してよいのは`<STAGING_DIR>`内のrestrictedな一時fileだけであり、そこから抽出したsanitized fieldだけをdurable Final Reportへ転記します（§15参照）。
- 保持してよいのは、submission ID、status（`Accepted`／`Invalid`等）、submitted／completed timestamp、issue件数、normalized package-relative issue path、Appleが返す定型的なsafe issue codeだけです。
- username、home directory path、Apple ID、credential、local絶対pathをevidence summaryから除去します。`hdiutil attach`の生plist出力（§17）も同様に、抽出した`<DEVICE_IDENTIFIER>`の値以外はdurable evidenceへ含めません。
- rejected／timeoutになったsubmissionの中間artifact（未staple DMG等）は、`<FINAL_DIR>`とは分離した場所（`<STAGING_DIR>`内）へ置きます。
- これらのfailed-evidence artifactへfinalなfilename・checksumを付与しません（final release assetと誤認させないため）。
- 自動削除はせず、明示的な`failed-evidence`ディレクトリ（`<FINAL_DIR>`の外）へ保持します——原因調査のための証跡として残しますが、final assetとは物理的にも命名的にも区別します。

## 29. Deferred automation

PB-3o.3 Slice 2 automation attemptは、独立review（PB-3o.3a）でrelease-safety findingsが報告され、focused correction（PB-3o.3a.1）後も複雑性と追加findingsが残ったため、**未commitのまま**repository外のbounded backupへ退避し、working treeをclean Slice 1 HEADへ復元しました（PB-3o.3c）。この経緯の詳細は[ROADMAP.md](ROADMAP.md)を参照してください。

完全自動化されたrelease workflow（本procedureのstepをGoまたはshellで自動実行するもの）は、将来の`M-RELEASE-1`Checkpointとして、initial Public Beta公開後に別途設計・実装します。今回のmanual procedureの存在を、その将来automationがすでに製品architectureへ実装済みであることの根拠にしないでください。
