# Public Beta Release Checklist

Candidate: `v1.0.0-beta.1`

このchecklistはPublic Betaを外部へ配布する直前に、release ownerが全項目を記録するためのものです。Mock／temporary環境で自動確認できる項目と、実機・実serviceが必要な項目を混在させません。

## 1. Human decisions — release前に必須

- [x] 公開製品名を`WorkCairn`、binaryを`workcairn*`、Go moduleを`github.com/AkiraShimizu0/workcairn/go`と決定した。
- [ ] GitHub repositoryを`workcairn`へ実renameし、clone URLとmodule pathの一致を確認した。
- [ ] 配布予定地域で`WorkCairn`の正式な商標clearanceを完了した。
- [ ] Private Vulnerability Reportingを有効化し、`SECURITY.md`の報告経路を実際に確認した。
- [x] Public Betaのsupport窓口とresponse expectationを[Release Notes](ReleaseNotes.md)へ記載した（Issues／Discussions／Private Vulnerability Reporting、GitHub上へ集約）。support emailは必須としない。
- [x] 初期Public Beta配布対象をmacOS／arm64（Tier 1）だけとし、他targetはnative smoke完了後の追加candidateとすることをHuman Operatorが決定した。iPhone、複数Mac／VM、Obsidian、iCloud Driveはいずれも初期Public Betaの必須acceptance条件としない。
- [ ] tag、Release title、CHANGELOG、archiveのversionを`v1.0.0-beta.1`へ揃えた。CHANGELOG本体は現HEADまで同期済み（PHASE PB-2）。tag作成とRelease title確定はrelease owner承認後に行う。

## 2. Supported platform matrix

| Target | Build | Native CLI／filesystem smoke | daemon smoke | iPhone smoke | Beta配布可否 |
|---|---:|---:|---:|---:|---:|
| darwin/arm64 | automated | required | required | required | Tier 1 candidate |
| darwin/amd64 | automated | required on Intel Mac | required | optional | native確認後 |
| linux/amd64 | automated | required on Linux | required | not required | native確認後 |
| linux/arm64 | automated | required on Linux arm64 | required | not required | native確認後 |
| windows/* | excluded | unsupported file lock | unsupported | unsupported | 配布しない |

cross-build成功だけでnative supportを宣言しません。初期Public Betaはdarwin/arm64だけを配布対象とする（PB-2で決定済み）。他3 targetのnative smokeはBeta必須条件ではなく、それらを配布対象へ追加する際の将来candidateとして扱う。

## 3. Automated — Mock／temporaryだけで確認

- [ ] `make public-beta-smoke`が成功する。
- [ ] `make v1-release-gate`が成功する。
- [ ] 全Go test、race、vet、gofmtが成功する。
- [ ] macOS／Linuxの4 target、3 binaryがCGOなしでcross-buildできる。
- [ ] temporary Vault + Mock ProviderでTask execution、Deliverable、Auditが完了する。
- [ ] mobile Interactionで依頼、clarification、Plan承認、Reviewed Workflow完了まで成功する。
- [ ] 空のtemporary rootでFirst-run Wizardを開き、明示承認前は副作用ゼロ、承認後はStarter Organizationが既存writerで作成される。
- [ ] 同一Session／Versionのpollingを複数回行ってもtext、select、focus、開いている詳細が保持される。
- [ ] failure／partial failureがToastで消えず、My Actions、依頼一覧、Timelineからsanitized detailを再確認できる。
- [ ] Workflow承認対象へAutonomy Contractが固定され、Proof of Work／CEO Attentionがcanonical evidenceから再構成される。
- [ ] Request Changes、Revision、再Review、Command replayが成功する。
- [ ] 承認なしのProvider／Vault effectが拒否される。
- [ ] JSON Contract v1、Prompt／Markdown／migration fixtureが成功する。
- [ ] retired runtime asset guardとarchitecture ownership gateが成功する。

## 4. Archive and checksum

targetごとにclean output directoryを使います。

- [ ] `make release-package RELEASE_GOOS=<os> RELEASE_GOARCH=<arch> BUILD_DATE=<RFC3339>`が成功する。darwin archiveはSecurity.frameworkをlinkするためmacOS hostで作成する。
- [ ] archive名、root directory、`VERSION`、3 binaryのversion、commit、build dateが一致する。
- [ ] macOSの`shasum -a 256 -c`またはLinuxの`sha256sum -c`でchecksumが成功する。
- [ ] archiveは3 binary、`VERSION`、LICENSE、README、CHANGELOG、SECURITY、CONTRIBUTING、docsだけを含む。
- [ ] `make verify-release-package ARCHIVE=<absolute archive path>`が成功する。
- [ ] source、test、fixture、`.git`、`.env`、Vault、cache、temporary file、local build outputを含まない。
- [ ] 展開後のbinaryがarchive外のruntimeやSDKを要求しない。

## 5. Clean install and first run

各native targetの新しいuser accountまたはclean runnerで確認します。

- [ ] checksum確認、展開、3 binaryのversion表示が成功する。
- [ ] 空のtemporary Vaultでloopback daemonが起動する。
- [ ] `/healthz`と`/readyz`が成功する。
- [ ] credentialなしでUIとread-only inspectionへ到達できる。
- [ ] GUI Wizardだけで選択済みrootのlayoutとStarter Organizationを準備できる。
- [ ] `--vault`なしのmacOS first-runでnative pickerがiCloud Driveを推奨し、明示選択した空の専用folderだけを保存する。
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

- [ ] `--mobile`がprivate addressとpairing codeを表示した。
- [ ] iPhone Safariでpairing、reload、background復帰、完了確認を実施した。
- [ ] iPhoneからAI Connection／Finder操作を開始できず、Macで行う案内だけが表示される。
- [ ] 承認済みbackground実行は小さいindicatorだけとなり、clarification／approval／failure／Recoveryだけが前面に出る。
- [ ] iPhoneでMy Actionsが既定となり、質問／承認以外では`No action needed`が明確に見える。

### Provider

- [ ] temporary Vaultとtest用credentialでPlan生成1回、Task1件、Review1回を実行した。
- [ ] Automatic policyが選ぶsupported Provider model、timeout、usage、error表示を確認した（利用者によるModel ID入力は不要）。
- [ ] credentialをVault、Command、log、shell history、screenshotへ残していない。
- [ ] test後にcredentialを失効またはrotationした。

### Filesystem／upgrade

- [ ] Human Operator自身のMacで、選択した専用Vault root（iCloud Driveは推奨だが必須ではなく、任意のローカルfolderでもよい）でFirst-runを完了し、既存個人Vaultが変更されていないことを確認した。Obsidianから開けることの確認は任意（Obsidianは必須dependencyではない）。
- [ ] [macOS First-run Acceptance](PublicBetaFirstRunAcceptance.md)を1回通し、再起動後のTimeline／persistent failureを確認した。
- [ ] 同一Vaultへ複数daemonをwriterとして起動していない。
- [ ] native filesystemでatomic replacement、file lock、CAS conflict、graceful shutdownを確認した。
- [ ] 実Vaultのcopyでread-only inventoryとmigration planだけを実行した。
- [ ] 実Vault本体は変更せず、backup／restore手順を別に確認した。

## 7. Security and privacy review

- [ ] tracked fileとarchiveにsecret、private key、実Provider responseがない。
- [ ] 人名、社員情報、Project名、Vault path、username、home directory等の個人／machine固有情報がfixture以外にない。
- [ ] fixtureの人物、Project、credential、timestampは明示的なfakeである。
- [ ] daemonの既定loopback、mobile private-address制約、same-origin effect requestがtestで固定される。
- [ ] remote authentication、TLS、Push、automatic retry、reconciliationを未実装として明記する。
- [ ] WordPress credentialをRuntime環境だけから受け取り、evidenceへ保存しない。

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

- [ ] `git status`がcleanで、release commit SHAを記録した。
- [ ] Release Gate結果、native smoke結果、未対応targetをRelease noteへ添付した。
- [ ] blocker、known limitation、Recovery boundaryを隠していない。
- [ ] tag／push／Public化はrelease ownerの明示承認後だけ実施する。

## Product name checkpoint

正式名称は`WorkCairn`です。Public surfaceのrename境界は[ProductNaming.md](ProductNaming.md)と[ADR-0034](adr/ADR-0034-workcairn-brand-and-living-company-dashboard.md)へ記録済みです。Public Beta前に実GitHub repository rename、商標／domain／handle確認、実機での初見UX確認を完了します。
