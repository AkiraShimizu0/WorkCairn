# Public Beta Release Checklist

Candidate: `v1.0.0-beta.1`

このchecklistはPublic Betaを外部へ配布する直前に、release ownerが全項目を記録するためのものです。Mock／temporary環境で自動確認できる項目と、実機・実serviceが必要な項目を混在させません。

## 1. Human decisions — release前に必須

- [x] 公開製品名を`WorkCairn`、binaryを`workcairn*`、Go moduleを`github.com/AkiraShimizu0/WorkCairn/go`と決定した。
- [x] GitHub repositoryを`WorkCairn`へ実rename済み（`origin`は`git@github.com:AkiraShimizu0/WorkCairn.git`）。clone URLはPHASE PB-2.1で`https://github.com/AkiraShimizu0/WorkCairn.git`へ更新済み。Go module pathはPHASE PB-2.2で`github.com/AkiraShimizu0/WorkCairn/go`（大文字混在）へ揃え、GitHub canonical repository pathとの大文字小文字差異を解消した（245 Go source file・821箇所のimport pathとMakefile／release scriptsのldflags参照を機械的に更新、`go.sum`・JSON Contract・Vault／`workspace-*` identifierは無変更）。実際のnetwork経由`go get`検証は引き続き未実施（この製品はsourceのclone + `make go-build`で配布し、`go get`によるlibrary的消費を想定していないため）。
- [ ] 配布予定地域で`WorkCairn`の正式な商標clearanceを完了した。
- [x] Private Vulnerability Reportingを有効化した（PHASE PB-2.33）。`SECURITY.md`の報告経路の実際の確認は継続対象。
- [x] Public Betaのsupport窓口とresponse expectationを[Release Notes](ReleaseNotes.md)へ記載した（Issues／Discussions／Private Vulnerability Reporting、GitHub上へ集約）。support emailは必須としない。
- [x] 初期Public Beta配布対象をmacOS／arm64（Tier 1）だけとし、他targetはnative smoke完了後の追加candidateとすることをHuman Operatorが決定した。iPhone、複数Mac／VM、Obsidian、iCloud Driveはいずれも初期Public Betaの必須acceptance条件としない。
- [ ] tag、Release title、CHANGELOG、archiveのversionを`v1.0.0-beta.1`へ揃えた。CHANGELOG本体は現HEADまで同期済み（PHASE PB-2）。[ADR-0071](adr/ADR-0071-macos-developer-id-signing-and-notarization.md)「6」のcanonical Release sequenceにより、local tag作成（final candidate HEAD確定後・build前）、tag push（remote公開）、GitHub Release作成は、それぞれ別のrelease owner明示承認を必要とする3つの独立したstepとなった。tag pushとGitHub ReleaseはHuman Acceptance完了前には行わない。
- [x] macOS配布はDeveloper ID Application署名・Hardened Runtime・Apple notarizationを必須とし、ad-hoc署名／unsigned binaryのrelease archive化を恒久禁止する方針を[ADR-0071](adr/ADR-0071-macos-developer-id-signing-and-notarization.md)として確定した（PHASE PB-3n）。Apple Developer Program加入、Developer ID証明書、`notarytool`profileの実在はいずれも未確認のHuman prerequisiteとして残る。署名・notarization実装、Human Acceptanceは§4a参照。

## 2. Supported platform matrix

| Target | Build | Native CLI／filesystem smoke | daemon smoke | Beta配布可否 |
|---|---:|---:|---:|---:|
| darwin/arm64 | automated | required | required | Tier 1 candidate |
| darwin/amd64 | automated | required on Intel Mac | required | native確認後 |
| linux/amd64 | automated | required on Linux | required | native確認後 |
| linux/arm64 | automated | required on Linux arm64 | required | native確認後 |
| windows/* | excluded | unsupported file lock | unsupported | 配布しない |

cross-build成功だけでnative supportを宣言しません。初期Public Betaはdarwin/arm64だけを配布対象とする（PB-2で決定済み）。他3 targetのnative smokeはBeta必須条件ではなく、それらを配布対象へ追加する際の将来candidateとして扱う。

`make public-beta-browser-gate`（Playwright WebKit・iPhone viewport含む）は、配布targetごとのsmokeではなく、Public Beta candidate全体に対して1回実行する必須のautomated Gateです。物理iPhone実機でのHuman Acceptanceはこれとは別で、§6「iPhone（任意機能の確認）」のとおりPublic Beta必須ではありません。

## 3. Automated — Mock／temporaryだけで確認

PHASE PB-2でcommit `b64caa9`（当時のHEAD）に対し全項目を確認済み。以降のPHASE PB-2.1〜PB-2.10でREADME、CONTRIBUTING、UI theme、その他public docsへ追加の変更が入っているため、この確認はtag対象commitの決定後に再実施が必要です。

- [x] `make public-beta-smoke`が成功する。
- [x] `make v1-release-gate`が成功する。
- [x] 全Go test、vet、gofmtが成功する。raceは`v1-release-gate`実行では成功。既知flaky pair（`TestRunParallelProviderCallBudgetStopsBeforeExceedingLimit`、`TestRunParallelBudgetPartialFailurePreservesOtherBranches`）は独立実行で1件非決定的に失敗する場合があり、retry-until-greenはしない既存方針のまま。
- [x] macOS／Linuxの4 target、3 binaryがcross-buildできる（`v1-release-gate`の`public-beta-build-matrix`）。darwin targetはmacOS host上でCGO有効、Linux targetはCGO無効。
- [x] temporary Vault + Mock ProviderでTask execution、Deliverable、Auditが完了する。
- [x] mobile Interactionで依頼、clarification、Plan承認、Reviewed Workflow完了まで成功する。
- [x] 空のtemporary rootでFirst-run Wizardを開き、明示承認前は副作用ゼロ、承認後はStarter Organizationが既存writerで作成される。
- [x] 同一Session／Versionのpollingを複数回行ってもtext、select、focus、開いている詳細が保持される。
- [x] failure／partial failureがToastで消えず、My Actions、依頼一覧、Timelineからsanitized detailを再確認できる。
- [x] Workflow承認対象へAutonomy Contractが固定され、Proof of Work／CEO Attentionがcanonical evidenceから再構成される。
- [x] Request Changes、Revision、再Review、Command replayが成功する。
- [x] 承認なしのProvider／Vault effectが拒否される。
- [x] JSON Contract v1、Prompt／Markdown／migration fixtureが成功する。
- [x] retired runtime asset guardとarchitecture ownership gateが成功する。
- [x] `make public-beta-browser-gate`（Chromium desktop + WebKit iPhone、フルsuite）が成功する。全required testが成功し、skipは既知の1件のみ（iPhone-project限定、desktop-pointer固有assertion）。

## 4. Archive and checksum

targetごとにclean output directoryを使います。PHASE PB-2でdarwin/arm64（Tier 1）の実archiveを1回build・検証済み（commit `b64caa988bb6`、version `v1.0.0-beta.1`）。このarchiveはPHASE PB-2時点の検証サンプルであり、final release archiveではありません。実際に配布するarchiveは、final cross-reviewと全修正完了後にtag対象として確定したcommitから改めてbuildしてください。

**注意（PHASE PB-3n）**: 以下の完了済みチェックはすべて、現行の**ad-hoc署名tar.gz**（`scripts/package-release.sh`の現行実装）に対するものです。[ADR-0071](adr/ADR-0071-macos-developer-id-signing-and-notarization.md)が決定したDeveloper ID署名・notarization・DMG containerへの移行が実装された後の最終release assetは、この節のチェックを自動的には満たしません。署名・notarization実装後は§4aの新しいチェックを満たす必要があります。

- [x] `make release-package RELEASE_GOOS=darwin RELEASE_GOARCH=arm64 BUILD_DATE=<RFC3339>`が成功する（macOS hostでSecurity.frameworkをlink）。
- [x] archive名、root directory、`VERSION`、3 binaryのversion、commit、build dateが一致する（3 binary全てが`v1.0.0-beta.1`／`b64caa988bb6`を報告）。
- [x] `make verify-release-package ARCHIVE=<absolute archive path>`（内部で`shasum -a 256 -c`）でchecksumが成功する。
- [x] archiveは3 binary、`VERSION`、LICENSE、README、CHANGELOG、SECURITY、CONTRIBUTING、docsだけを含む（`tar -tzf`で目視確認）。
- [x] source、test、fixture、`.git`、`.env`、Vault、cache、temporary file、local build outputを含まない。
- [x] 展開後のbinaryがarchive外のruntimeやSDKを要求しない（temporary directoryへ展開し3 binaryを直接実行して確認）。

## 4a. macOS Developer ID Signing and Notarization（ADR-0071 — 未実施）

以下は[ADR-0071](adr/ADR-0071-macos-developer-id-signing-and-notarization.md)が決定した方針の実施・検証項目です。すべて未実施です。実施完了までPublic Beta macOS配布はGatekeeperの通常経路を通りません。

**PHASE PB-3p.1で方針を更新しました**: initial Public Beta releaseは、未commitのまま複雑性と追加findingsが残ったPB-3o.3 Slice 2 automation attemptではなく、[Manual macOS Signed Release Procedure](ManualMacOSReleaseProcedure.md)に沿ってHumanが1 stepずつApple標準commandを明示実行するbounded manual procedureで生成します（ADR-0071 PB-3p.1 addendum参照）。この方針変更は、以下いずれの項目も緩和しません。automation実装の有無はAcceptance条件の代替にも免除事由にもならず、**signed／notarized／stapled／quarantined DMGが生成されたこと**が条件です。

- [ ] Apple Developer Program（またはEnterprise Program）加入とAccount Holder権限をHumanが確認した。
- [ ] Developer ID Application証明書を取得し、Humanのbuild host macOS Keychainへ保持した。SHA-1 fingerprint、有効期限、revocation状態を記録した。
- [ ] 実Team IDを確認し、`notarytool` profile作成時に同じTeam IDを使用したevidenceを記録した。
- [ ] `notarytool` Keychain profile（名前のみ）をHumanが作成した（Apple ID／app-specific passwordまたはApp Store Connect API keyはWorkCairn scriptへもAIエージェントへも渡さない）。
- [ ] [Manual macOS Signed Release Procedure](ManualMacOSReleaseProcedure.md)に沿って、ADR-0071のcanonical identifier（`com.workcairn.cli.workcairn`／`com.workcairn.cli.workcairn-daemon`／`com.workcairn.cli.workcairn-core`）とHardened Runtime・secure timestampで3 binaryをHumanが署名し、各stepの結果を確認した。
- [ ] canonical macOS distribution containerがADR-0071「5」固定contractのDMG（UDIF／UDZO、単一volume、`com.workcairn.dist.macos`で署名）へ移行し、`notarytool submit ... --wait --timeout 90m`（native timeout contract、client-side wait上限90分、ADR-0071「7」と一致）でnotarizationされ、`stapler staple`でticketが添付される。`--timeout`を省いたunbounded commandでこの項目を完了扱いにはできない。timeout発生時のHuman判断待ちという境界はADR-0071「7」のまま維持する。
- [ ] 生成したDMGに対し、[Manual macOS Signed Release Procedure](ManualMacOSReleaseProcedure.md)の該当stepで、ADR-0071「11」の5分類（Offline deterministic／Apple service evidence／Local policy evidence／Human evidence／Static fake-tool test）に相当する確認をHumanが実施した。単一区分だけをもってPASSとしない。
- [ ] New-user Keychain Acceptance（[PublicBetaFirstRunAcceptance.md](PublicBetaFirstRunAcceptance.md) §C、ADR-0071「8」）: signed build Nで初回Keychain登録・read-backが成功する。
- [ ] Upgrade Keychain Acceptance（[PublicBetaFirstRunAcceptance.md](PublicBetaFirstRunAcceptance.md) §D、ADR-0071「9」）: signed build N→N+1でcredential再入力なしにKeychain readが成功し、designated requirementとN/N+1 provenanceが記録された。
- [ ] Gatekeeper／quarantined download Acceptance（[PublicBetaFirstRunAcceptance.md](PublicBetaFirstRunAcceptance.md) §E、ADR-0071「10」）: DMG層・3 CLI層の両方でGatekeeperの通常経路が確認された（いずれか一方だけでは不可）。
- [ ] signed buildに対するProvider Plan／Task／Review Acceptanceを別途実施した（[PublicBetaFirstRunAcceptance.md](PublicBetaFirstRunAcceptance.md)セクションA自体はad-hoc署名build時代のhistorical evidenceであり、この必須条件を代替しない）。

## 5. Clean install and first run

各native targetの新しいuser accountまたはclean runnerで確認します。

- [ ] checksum確認、展開、3 binaryのversion表示が成功する。
- [ ] 空の一時的なデータフォルダでloopback daemonが起動する。
- [ ] `/healthz`と`/readyz`が成功する。
- [ ] credentialなしでUIとread-only inspectionへ到達できる。
- [ ] GUI Wizardだけで選択済みrootのlayoutとStarter Organizationを準備できる。
- [ ] `--vault`なしのmacOS first-runでnative pickerが通常のローカル保存場所に空の専用データフォルダを選ばせる（iCloud Driveは任意で推奨しない）。明示選択したfolderだけを保存する。
- [ ] daemon再起動時にfolder pickerを再表示せず、保存済みrootを再検証して過去Session／Timelineを表示する。
- [ ] Mac native hidden-inputからClaudeをKeychainへ接続し、iPhone、HTTP payload、browser storage、Vault、logへsecretを出さない。
- [ ] credentialが必要な操作は秘密情報を表示せず安全に拒否される。
- [ ] SIGINT／SIGTERMでgraceful shutdownする。
- [ ] install／first-run手順だけで暗黙のmachine固有pathを要求しない。

## 6. Real environment — 自動Gateでは代替不可

### macOS（Tier 1 — 必須）

Public Beta acceptanceの必須条件はHuman Operator自身のMacでのpackaged binary確認だけであり、物理iPhoneは不要。

- [ ] Human Operator自身のMacでdaemonを起動し、Company View（既定）で一般向け「進め方」、Timeline、persistent error詳細が読め、内部IDは詳細へ退いていることを確認した。
- [ ] Company ViewでMaker、Reviewer、Revision、担当、handoffを理解できることを確認した。
- [ ] Workflow承認で任せる範囲が短く理解でき、Proof of Workが「何が実際に完了したか」を技術語なしで説明することを確認した。
- [ ] public／shared network、port forwarding、internet公開を使っていない。

### iPhone（任意機能の確認 — Public Beta必須ではない）

iPhone Web UIはavailableな任意機能であり、Public Beta acceptanceの必須条件ではない。Mac browserだけでも一般UIの正式経路を完結できる。iPhoneを実際に使う場合だけ、以下を追加確認する。

- [ ] `--local-network`がprivate addressとpairing codeを表示した。
- [ ] iPhone Safariでpairing、reload、background復帰、完了確認を実施した。
- [ ] iPhoneからAI Connection／Finder操作を開始できず、Macで行う案内だけが表示される。
- [ ] 承認済みbackground実行は小さいindicatorだけとなり、clarification／approval／failure／Recoveryだけが前面に出る。
- [ ] iPhoneでMy Actionsが既定となり、質問／承認以外では対応が必要な項目がないことが明確に見える。

### Provider

signed build（[Manual macOS Signed Release Procedure](ManualMacOSReleaseProcedure.md)によるDMG署名・notarization完了後）に対して実施する。ad-hoc build時代の実施記録（[PublicBetaFirstRunAcceptance.md](PublicBetaFirstRunAcceptance.md)セクションA）はこの条件を代替しない。

- [ ] 空の一時的なデータフォルダとtest用credentialでPlan生成1回、Task1件、Review1回を実行した。credentialは本Acceptance専用とし、他のAcceptance（New-user／Upgrade）とは共有しない。
- [ ] Automatic policyが選ぶsupported Provider model、timeout、usage、error表示を確認した（利用者によるModel ID入力は不要）。
- [ ] credentialをVault、Command、log、shell history、screenshotへ残していない。
- [ ] test後にcredentialを失効またはrotationした。

### Filesystem／upgrade

- [ ] Human Operator自身のMacで、選択した専用データフォルダ（通常のローカル保存場所。iCloud Driveは任意で推奨しない）でFirst-runを完了し、既存個人Vaultが変更されていないことを確認した。Obsidianから開けることの確認は任意（Obsidianは必須dependencyではない）。
- [ ] [macOS First-run Acceptance](PublicBetaFirstRunAcceptance.md)セクションA（historical、ad-hoc署名build時代のMac loopback手順）を1回通し、再起動後のTimeline／persistent failureを確認した。**これ単独ではPublic Beta GO条件を満たさない**——signed buildに対するセクションC／D／EとProvider Acceptanceが別途必須（§4a参照）。
- [ ] 同一データフォルダへ複数daemonをwriterとして起動していない。
- [ ] SIGINT／SIGTERMでgraceful shutdownすることを確認した。

任意／deferred。Public Beta blockerではない：

- [ ] native filesystemでatomic replacement、file lock、CAS conflictの追加stressを確認する。
- [ ] 実Vaultのcopyでread-only inventoryとmigration planだけを実行する。
- [ ] 実Vault本体を変更しないbackup／restore演習を別に確認する。

## 7. Security and privacy review

- [x] tracked fileとarchiveにsecret、private key、実Provider responseがない（PB-1の全history／tracked file secret scanと、PB-2の実archive内容確認で確認済み）。
- [x] 人名、社員情報、Project名、Vault path、username、home directory等の個人／machine固有情報がfixture以外にない（PB-1で確認済み）。
- [x] fixtureの人物、Project、credential、timestampは明示的なfakeである（PHASE PB-2.17のGit管理対象監査で実データ漏えいの証拠なし、repository ownerがsynthetic dataであることを確認、[CONTRIBUTING.md](../CONTRIBUTING.md)／[CONTRIBUTING.ja.md](../CONTRIBUTING.ja.md)へtest-data provenance policyとして記録）。
- [x] daemonの既定loopback、local-network private-address制約、same-origin effect requestがtestで固定される（既存test、PB-2の全Go test再実行で確認）。
- [x] remote authentication、TLS、Push、automatic retry、remote Action reconciliationを未実装として明記する（README、SECURITY.md、OperatorGuide.md、Release Notesに記載済み）。
- [x] WordPress credentialをRuntime環境だけから受け取り、evidenceへ保存しない（OperatorGuide.mdに記載済み、既存testで確認）。

## 8. Public repository files

- [x] MIT `LICENSE`
- [x] `README.md`
- [x] `SECURITY.md`
- [x] `CONTRIBUTING.md`
- [x] `CHANGELOG.md`
- [x] Architecture、Operator、Recovery、HTTP、Release Gate docs
- [x] GitHub issue／PR templates、`CODE_OF_CONDUCT.md`、`SUPPORT.md`はoptional／deferredとする（PB-2）。採用しない判断でもPublic Beta開始を妨げない。採用する場合は別途Human判断で追加する。
- [ ] repository description、topics、screenshots、support boundaryを確認した。

## 9. Final release sign-off

repository Public化とPrivate Vulnerability Reporting有効化はPHASE PB-2.33で完了済みです（§1参照）。以下はいずれも未実施です。

- [ ] `git status`がcleanで、release commit SHAを記録した。
- [ ] Release Gate結果、native smoke結果、未対応targetをRelease noteへ添付した。
- [ ] blocker、known limitation、Recovery boundaryを隠していない。
- [ ] [ADR-0071](adr/ADR-0071-macos-developer-id-signing-and-notarization.md)「6」のcanonical Release sequenceに従い、local tag作成をrelease ownerが明示承認した。
- [ ] Human Acceptance（New-user／Upgrade／Gatekeeper／Provider、§4a・§6）完了後、tag push（remote公開）とGitHub Release作成をrelease ownerが別途明示承認した。tag push・GitHub Release作成のいずれもAcceptance完了前には行わない。

## Product name checkpoint

正式名称は`WorkCairn`です。Public surfaceのrename境界は[ProductNaming.md](ProductNaming.md)と[ADR-0034](adr/ADR-0034-workcairn-brand-and-living-company-dashboard.md)へ記録済みです。Public Beta前に実GitHub repository rename、商標／domain／handle確認、実機での初見UX確認を完了します。
