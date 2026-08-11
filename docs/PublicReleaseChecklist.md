# Public Beta Release Checklist

Candidate: `v1.0.0-beta.1`

このchecklistはPublic Betaを外部へ配布する直前に、release ownerが全項目を記録するためのものです。Mock／temporary環境で自動確認できる項目と、実機・実serviceが必要な項目を混在させません。

## 1. Human decisions — release前に必須

- [x] 公開製品名を`WorkCairn`、binaryを`workcairn*`、Go moduleを`github.com/AkiraShimizu0/workcairn/go`と決定した。
- [ ] GitHub repositoryを`workcairn`へ実renameし、clone URLとmodule pathの一致を確認した。
- [ ] 配布予定地域で`WorkCairn`の正式な商標clearanceを完了した。
- [ ] Private Vulnerability Reportingを有効化し、`SECURITY.md`の報告経路を実際に確認した。
- [ ] Public Betaのsupport窓口とresponse expectationをRelease noteへ記載した。
- [ ] 配布対象をTier 1だけにするか、native smoke済みcandidateを追加するか決定した。
- [ ] tag、Release title、CHANGELOG、archiveのversionを`v1.0.0-beta.1`へ揃えた。

## 2. Supported platform matrix

| Target | Build | Native CLI／filesystem smoke | daemon smoke | iPhone smoke | Beta配布可否 |
|---|---:|---:|---:|---:|---:|
| darwin/arm64 | automated | required | required | required | Tier 1 candidate |
| darwin/amd64 | automated | required on Intel Mac | required | optional | native確認後 |
| linux/amd64 | automated | required on Linux | required | not required | native確認後 |
| linux/arm64 | automated | required on Linux arm64 | required | not required | native確認後 |
| windows/* | excluded | unsupported file lock | unsupported | unsupported | 配布しない |

cross-build成功だけでnative supportを宣言しません。

## 3. Automated — Mock／temporaryだけで確認

- [ ] `make public-beta-smoke`が成功する。
- [ ] `make v1-release-gate`が成功する。
- [ ] 全Go test、race、vet、gofmtが成功する。
- [ ] macOS／Linuxの4 target、3 binaryがCGOなしでcross-buildできる。
- [ ] temporary Vault + Mock ProviderでTask execution、Deliverable、Auditが完了する。
- [ ] mobile Interactionで依頼、clarification、Plan承認、Reviewed Workflow完了まで成功する。
- [ ] Workflow承認対象へAutonomy Contractが固定され、Proof of Work／CEO Attentionがcanonical evidenceから再構成される。
- [ ] Request Changes、Revision、再Review、Command replayが成功する。
- [ ] 承認なしのProvider／Vault effectが拒否される。
- [ ] JSON Contract v1、Prompt／Markdown／migration fixtureが成功する。
- [ ] retired runtime asset guardとarchitecture ownership gateが成功する。

## 4. Archive and checksum

targetごとにclean output directoryを使います。

- [ ] `make release-package RELEASE_GOOS=<os> RELEASE_GOARCH=<arch> BUILD_DATE=<RFC3339>`が成功する。
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
- [ ] credentialが必要な操作は秘密情報を表示せず安全に拒否される。
- [ ] SIGINT／SIGTERMでgraceful shutdownする。
- [ ] install／first-run手順だけで暗黙のmachine固有pathを要求しない。

## 6. Real environment — 自動Gateでは代替不可

### macOS／iPhone

- [ ] Tier 1 MacとiPhoneを同じtrusted Wi-Fiへ接続した。
- [ ] `--mobile`がprivate addressとpairing codeを表示した。
- [ ] iPhone Safariでpairing、reload、background復帰、完了確認を実施した。
- [ ] iPhoneでMy Actionsが既定となり、質問／承認以外では`No action needed`が明確に見える。
- [ ] iPad／MacでCompany Viewが既定となり、Maker、Reviewer、Revision、担当、handoffを理解できる。
- [ ] iPhoneのWorkflow承認で任せる範囲が短く理解でき、Company ViewのProof of Workが「何が実際に完了したか」を技術語なしで説明する。
- [ ] public／shared network、port forwarding、internet公開を使っていない。

### Provider

- [ ] temporary Vaultとtest用credentialでPlan生成1回、Task1件、Review1回を実行した。
- [ ] Provider model ID、timeout、usage、error表示を確認した。
- [ ] credentialをVault、Command、log、shell history、screenshotへ残していない。
- [ ] test後にcredentialを失効またはrotationした。

### Filesystem／upgrade

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
- [ ] GitHub issue／PR templatesとCode of Conductを採用するか人間が決定した。
- [ ] repository description、topics、screenshots、support boundaryを確認した。

## 9. Final release sign-off

- [ ] `git status`がcleanで、release commit SHAを記録した。
- [ ] Release Gate結果、native smoke結果、未対応targetをRelease noteへ添付した。
- [ ] blocker、known limitation、Recovery boundaryを隠していない。
- [ ] tag／push／Public化はrelease ownerの明示承認後だけ実施する。

## Product name checkpoint

正式名称は`WorkCairn`です。Public surfaceのrename境界は[ProductNaming.md](ProductNaming.md)と[ADR-0034](adr/ADR-0034-workcairn-brand-and-living-company-dashboard.md)へ記録済みです。Public Beta前に実GitHub repository rename、商標／domain／handle確認、実機での初見UX確認を完了します。
