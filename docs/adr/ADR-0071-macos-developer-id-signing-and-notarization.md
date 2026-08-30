# ADR-0071: macOS Developer ID Signing and Notarization Architecture

## Status

Accepted（設計・方針決定のみ。署名・notarization実装、実Apple account操作はこのADR自体では未実施）

PHASE PB-3n.1のCodex独立reviewでP1 5件・関連P2を報告され、PHASE PB-3n.2で本文をfocused correctionしました（Release順序、identity contract、Section A GO表現、Gatekeeper層分離、Upgrade N/N+1 provenance、Apple要件とWorkCairn方針の分離、causality表現、DMG contract、notary timeout／evidence contract、verifier evidence分類）。PHASE PB-3n.3のCodex focused reviewでP2 2件（証明書rotation時のSHA-1 fingerprint説明の不正確さ、GNU `timeout`への非標準依存）が報告され、PHASE PB-3n.4で本文をfocused correctionしました（Certificate rotation契約の追加、notarization bounded wait deadlineをGo-owned supervisorまたはnotarytool native timeout optionで実装する契約への修正）。PHASE PB-3n.5のCodex focused reviewでP1 1件（既存署名済みartifactの有効性を無条件に断定していたこと）・P2 2件（新証明書fingerprintのCSR／鍵ペア例外の不正確さ、native `notarytool --timeout`が利用可能なのにGo supervisorとの二択を残していたこと）が報告され、PHASE PB-3n.5aで本文をfocused correctionしました（fingerprintの継続を鍵ペア・CSR等から推測しない契約への修正、certificate expiry／rotation／certificate revocation／notarization ticket revocationの区別、canonical timeout contractをnative `--wait --timeout`一本化しGo supervisor分岐を撤回、timeout時のsubmission ID可用性を未検証事項として明記）。PHASE PB-3n.5bのCodex focused reviewでP0 0件・P1 0件・P2 2件・P3 0件が報告されました（canonical Release sequence step 9とPublic Release Checklistからmandatory `--timeout`が欠落していたこと、`90m`がhelpで確認済みであるにもかかわらずduration syntaxを未確定のまま残していたこと）。PHASE PB-3n.5cで、canonical invocationを`notarytool submit ... --wait --timeout 90m`へ統一しました。Developer ID／Hardened Runtime／notarization／DMGという基本方針自体は変更していません。

## Context

現在のmacOS packaged `workcairn`／`workcairn-daemon`／`workcairn-core`は、`scripts/package-release.sh`がGo linkerによるad-hoc署名のままtar.gzへ収めるだけです（`codesign`呼び出しなし）。PB-3jのHuman Packaged-Binary Acceptanceで、旧buildが作成したmacOS Keychain項目を新buildが読めず、`workcairn-daemon`がlistener開始前に停止しました。PB-3kのread-only診断（実binary再実行・Keychain変更なし）で次を確認しています。

- 両binaryとも`codesign -dvv`相当の出力で`Signature=adhoc`、`TeamIdentifier=not set`、`Identifier=a.out`（Go linkerの既定値、明示`-i`指定なし）、`Internal requirements=none`
- 旧buildと新buildでcode hash（CDHash）が異なる
- 両binaryともlocal `spctl --assess`でreject
- `scripts/package-release.sh`はbuild後にtar.gzを作るだけで、`scripts/verify-release-archive.sh`はDeveloper ID、Team ID、identifier、Hardened Runtime、notarization、Gatekeeperのいずれも検証しない

**この時点で確定している事実は「ad-hoc署名build間でKeychain継続アクセスが失敗した」「CDHash／designated requirementがbuild間で異なる」ことだけです。** macOS Keychain ACLのcode identity mismatchが、PB-3jの停止に対する証拠に基づく最有力仮説ですが、実際のOSStatus、実際のACL評価内容は未確認であり、この仮説とPB-3jの停止との**direct causalityは未確定**です。本ADRは、この未確定のcausalityを断定することなく、Developer ID署名がGatekeeperの通常経路と証明書チェーンに基づく安定したidentityの両方を提供するために、いずれにせよ必要であるという設計判断に基づいてArchitectureを決定します。診断自体はPB-3kで終わっており、本Checkpointは恒久対策としてのDeveloper ID署名・notarization Architectureの設計・決定です。実Apple account、証明書、notarization profileの存在は未確認であり、本ADRはそれらの存在を仮定しません。

## Confirmed evidence

- 現在のmacOS release binaryはad-hoc署名、Team IDなし、明示code-signing identifierなし、Hardened Runtimeなし、notarizationなし（PB-3k診断）。
- ad-hoc署名build間でmacOS Keychain継続アクセスが実際に失敗した（PB-3j、1回の実観測）。
- 新旧buildでCDHash／designated requirementが異なることを確認済み（PB-3k、`codesign -dvv`相当の出力比較）。
- `scripts/package-release.sh`はGo binary buildとtar.gz packagingだけを行い、`codesign`／`notarytool`／`stapler`を呼ばない（source確認）。
- `scripts/verify-release-archive.sh`はchecksum、allow-listされたfile一覧、archive root名とVERSIONの一致だけを検証し、署名・notarization関連の検査を一切行わない（source確認）。
- `docs/adr/ADR-0044-native-macos-keychain-persistence.md`のKeychain Adapterは、service `com.workcairn.provider.anthropic`／account `api-key`固定でSecurity.frameworkを直接呼ぶ。この service識別子はcredential itemの識別であり、本ADRが扱うbinary code-signing identifierとは別の責務である。
- Apple公式資料（本ADR末尾に列挙）により、次を事実として確認済み。
  - Developer IDの利用にはApple Developer ProgramまたはApple Developer Enterprise Programへの加入が必要（[Developer ID](https://developer.apple.com/support/developer-id/)）。
  - macOS 10.15以降、2019年6月1日以降にbuildされDeveloper IDで配布される全softwareはnotarization必須（[Notarizing macOS software before distribution](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)）。
  - notarization submissionには、有効なcode署名、Developer ID証明書（ad-hoc／development／Apple Distribution証明書は不可）、Hardened Runtime、secure timestamp、macOS 10.9以降SDKでのlinkが必要（同上）。
  - `com.apple.security.get-task-allow`エンタイトルメントが**値`true`で存在すること**がnotarization rejectの原因である（同上）——このAppleの要件はkeyの値についての制約であり、key自体の完全な不在をAppleが常に要求しているわけではない（後述「4. Hardened Runtime and entitlements」参照）。
  - nonbundled実行ファイル（bundle構造を持たないCLI binary）へは`codesign -i <identifier>`で明示的なcode-signing identifierを与える必要がある。bundled codeはbundle IDが既定で使われるため不要（[Creating distribution-signed code for the Mac](https://developer.apple.com/documentation/xcode/creating-distribution-signed-code-for-the-mac/)）。
  - 同名のcode-signing identityが複数存在する場合は、SHA-1 hashで一意に指定できる（同上）。
  - Apple notary serviceが受け付けるcontainerはUDIF形式のdisk image、署名済みflat installer package、ZIP archiveの3種のみ。tar.gzは対象外（[Notarizing macOS software before distribution](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)）。
  - ZIP archiveは署名できず、ticketを直接stapleできない（archive内の個々のitemへstapleしてから再zipする必要がある）。disk imageとinstaller packageは署名でき、直接stapleできる（[Packaging Mac software for distribution](https://developer.apple.com/documentation/xcode/packaging-mac-software-for-distribution)）。
  - 複数containerを入れ子にする場合、notarizationは最も外側のcontainerだけへ行う（同上）。
  - `notarytool submit --keychain-profile <name>`を使うことで、Apple ID／app-specific password／App Store Connect API keyの実secretをscriptへcleartextで渡さずに済む。profileは`notarytool store-credentials`で事前に一度だけ作成する（[Customizing the notarization workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)）。
  - notarizationの標準scan処理は、Xcode経由のupload時「usually less than an hour」（同上、"Notarize your app automatically as part of the distribution process"）。別ページでは大半が5分以内、98%が15分以内に完了するとの記載がある（[Customizing the notarization workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)）。これらはApple公式の固定SLAではなく目安である。
  - `notarytool submit`はnative `--wait --timeout <duration>`をサポートする。Apple公式の[Customizing the Xcode archive process](https://developer.apple.com/documentation/security/customizing-the-xcode-archive-process)は、post-action scriptの例として`"$DEVELOPER_BIN_DIR/notarytool" submit -p "$AC_PASSWORD" --verbose "$DMG_PATH" --wait --timeout 2h --output-format plist`（コメント「Wait up to 2 hours for a response.」）を掲載しており、`--timeout`が実在するoption・時間指定syntaxの一例（`2h`）であることを確認できる。ただし、この`--timeout`到達時に`notarytool`が具体的に何を出力する（submission IDを含むか等）かは、閲覧した公式資料の範囲では明示されていない。
  - Developer ID Application証明書は、5年間有効。証明書のexpiry後もcompile時点で証明書が有効だったsoftwareはGatekeeperの通常経路で継続してdownload・実行できる（Developer ID provisioning profileを使わない場合。[Developer ID](https://developer.apple.com/support/developer-id/)）。一方、revokeされた証明書で署名されたDeveloper ID appは、既にinstall済みであってもinstallまたは起動ができなくなる（同上）。Developer ID Application証明書はApple Developer Programアカウントの通常操作から失効できず、`product-security@apple.com`への申請が必要（[Revoking privileges](https://developer.apple.com/help/account/reference/revoking-privileges)）。
  - notarization自体は、署名鍵の漏洩等の場合にAppleと協力してticketを無効化（revoke）できる仕組みを持つ（[Notarizing macOS software before distribution](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)）——これはcertificate revocationとは別の、notarization ticket自体のrevocationである。
  - notarization rejectの典型的原因は、無効な署名、非Developer ID証明書、secure timestamp欠落、Hardened Runtime未有効化、`get-task-allow=true`エンタイトルメントの存在、不正な形式のentitlements plistである（[Resolving common notarization issues](https://developer.apple.com/documentation/security/resolving-common-notarization-issues)）。

## Unconfirmed hypotheses（本ADRでは未検証のまま残す）

- Human OperatorのApple Developer Program加入状況、Account Holder権限、実Team ID。
- Developer ID Application証明書が実際に発行・macOS Keychainへ保持されているか。
- `notarytool` Keychain profileが実際に作成されているか。
- Developer ID署名後に実際に生成されるdesignated requirementの正確な内容。
- 証明書のrenewal／rotationを跨いだ場合の、既存Keychain項目に対するdesignated requirement互換性。
- **PB-3jのKeychain継続アクセス失敗と、macOS Keychain ACLのcode identity mismatchとのdirect causality**（PB-3kのままcircumstantial evidenceに留まる。「CDHash／DRが異なる」ことと「Keychain readが失敗した」ことは同時に観測された事実であり、前者が後者の直接原因であると証明されたわけではない）。
- 実際のOSStatus値、実際のKeychain ACL評価内容（PB-3kはread-only診断でここまで到達していない）。
- `notarytool`がclient側`--timeout`到達時にsubmission IDを出力（または既に取得済みの値として利用可能）にするかどうかの具体的な挙動（本ADR「7」参照。help／versionからの確認、fake-tool test、実notarization実行時のsanitized evidenceで明らかにする）。

これらはすべて、後続の実装Checkpointが実際のDeveloper ID署名binaryを生成して初めて検証できます。本ADRの設計は、これらが確認された後に机上の設計と食い違わないよう、確認ポイントを明示的なAcceptance手順として残します。**Developer ID署名は、上記causalityが最終的にどう確定するかに関わらず、Gatekeeperの通常配布経路自体が要求する必須要件であるため、本ADRの決定は上記の未確定性に左右されません。**

## Decision

WorkCairnのPublic Beta macOS配布は、Developer ID Application署名、Hardened Runtime、Apple notarizationを必須とします。ad-hoc署名やunsigned binaryをrelease archiveへ含めることを恒久的に禁止します。実装は別の後続Checkpointで行い、本ADRは方針とcontractだけを確定します。

### 1. Distribution identity — SHA-1／Team ID／profile契約

identity表示名（`Developer ID Application: <Team Name> (<TeamID>)`という文字列）の完全一致をcertificate選択の主契約にすることはやめます。表示名は人間が読み替え・typoし得る非decisiveな文字列だからです。代わりに、次の3つを独立した明示入力contractとして固定します。いずれもrepositoryへ保存せず、Humanが将来の実装へ明示的に供給します。

**`RELEASE_SIGNING_IDENTITY_SHA1`**

- Developer ID Application certificateのSHA-1 fingerprint。
- 正確に40桁の16進数文字列。大文字小文字の正規化（例: 大文字へ統一してから比較）は許容しますが、部分一致・prefix一致は禁止します。
- 将来の実装は`security find-identity -p codesigning -v`の出力からこのSHA-1と完全一致する行を探し、`codesign -s <SHA1>`でcertificateを一意指定します（SHA-1はcodesignの`-s`引数として直接使用可能）。
- 0件、複数件（通常はSHA-1が一意なので起きないはずですが、Keychain破損等の異常状態を想定しfail-closedにする）、invalid形式、または見つかったcertificateが有効期限切れの場合は、release packagingをfail-closedで停止します。
- private key本体やcertificate bodyそのものではなく、metadata識別子です。repositoryへ保存しません。
- Humanが明示的に供給します。WorkCairnのsourceやAIが推測・自動選択しません。

**`RELEASE_EXPECTED_TEAM_ID`**

- 署名後の3 CLI binaryとDMG containerの`TeamIdentifier`が全て一致すべき期待値。
- WorkCairnのsourceから推測しません（Team IDはHumanのApple Developer Program membershipに属する事実であり、repositoryのどの情報からも導出できません）。
- signing identity表示名からの曖昧parse（`(TEAMID)`のような括弧内文字列を正規表現で抜き出す等）はしません——表示名は将来変わり得る非decisiveな文字列だからです。
- 将来の実装は、実際に署名した後の`codesign -dvv`出力（`TeamIdentifier=`行）から取得した値と、この期待値を**完全一致**で照合します。3 CLIとDMGの4つの署名結果すべてが、期待値およびお互いに一致することを要求します。
- notary profile作成時にHumanが同じTeam IDを使用したことのevidenceを別途記録します（例: profile作成コマンドの実行記録、または`notarytool history --keychain-profile <profile>`相当の出力）。scriptがprofile内部に保存されたTeam IDを秘密裏に読み取れる、または読み取ってよいとは仮定しません（profileはApple ID／API key同様、secret扱いのopaqueな参照だからです）。
- notarization Accepted結果（Apple service側の検証）と、Human記録によるprofile evidence（WorkCairn側の記録）の両方を併用して、Team IDの一貫性を確認します——どちらか一方だけでは不十分です。
- mismatch時はrelease packagingを直ちに停止します。

**`RELEASE_NOTARY_PROFILE`**

- `notarytool` Keychain profileの名前だけ。secretではない参照です。
- profile未設定の場合はfail-closedで停止します。別のcredential種別への自動fallbackはしません。
- profile作成はHumanが対象build host上で`xcrun notarytool store-credentials`を直接実行する、完全にrepository外・WorkCairn script外の一度きりの作業です。WorkCairn（scriptもAIも）は`store-credentials`を実行しません。

**Certificate preflight**（将来の実装が署名前に必ず満たすべき条件）:

- `RELEASE_SIGNING_IDENTITY_SHA1`に一致する有効なDeveloper ID Application identityが、Keychain上に**exactly 1件**存在すること。
- そのcertificateのexpiry（有効期限）を記録すること。
- 署名時点でcertificateが有効期限内であること。
- revocation（失効）またはtrust failure（信頼チェーン検証失敗）は、`codesign`、notarization、Gatekeeperのいずれかの段階で拒否として扱うこと（WorkCairn独自のrevocation checkは実装せず、OSとApple notary serviceの既存検証に委ねる）。
- certificate display name（`security find-identity`が表示する人間可読名）はevidenceとして記録してよいが、selection key（証明書を選ぶための一致条件）としては使わない。
- private key material（秘密鍵の内容そのもの）を読み取り・export・logしない。
- 実際のsigning identity metadata（`security find-identity`の実出力等）をAIへread-only確認させる場合は、個別のHuman承認を必要とする（本ADR「Human prerequisites」参照）。

**Certificate rotation**（renewal／replacement／rotationとSHA-1 fingerprintの関係）:

- `RELEASE_SIGNING_IDENTITY_SHA1`は「今この瞬間、どの具体的なDeveloper ID Application証明書で署名するか」を一意に選択するための入力です。特定の1枚の証明書という不変の実体を指すものではありません。
- 証明書のrenewal（期限更新のための再発行）、replacement（差し替え）、rotation（定期的な入れ替え）では、Appleから**新しい証明書**が発行されます。新規発行された証明書は、たとえ同じ公開鍵を含む場合であっても、serial number、有効期間（Not Before／Not After）、issuerによる署名など証明書本体を構成する要素が異なる、別個のX.509 certificateです。certificate fingerprint（SHA-1）はこの証明書本体に対して計算される値であるため、**新しく発行された証明書は常に新しいfingerprintを持つものとして扱います**。同じ鍵ペア、同じCSR、同じTeam ID、同じdisplay nameであることを、fingerprintが継続する根拠にはしません。
- renewal、replacement、rotationのたびに、Humanが`security find-identity -p codesigning -v`等で新しいfingerprintを**再取得**し、`RELEASE_SIGNING_IDENTITY_SHA1`を**明示的に更新**します。過去のfingerprintから新しいfingerprintを自動導出する仕組みは持ちません。
- display nameによる曖昧な自動選択（例: 「同じTeam Nameの証明書ならどれでもよい」）は行いません——本ADR「1」の主契約どおり、SHA-1 fingerprintの完全一致だけを選択条件とします。
- 旧証明書、新証明書、または別のidentityへのhidden fallback（`RELEASE_SIGNING_IDENTITY_SHA1`が指す証明書が見つからない場合に別の有効なDeveloper ID Application証明書へ自動的に切り替えること）は禁止します。fingerprint完全一致で見つからない場合はfail-closedで停止します。
- `RELEASE_EXPECTED_TEAM_ID`も、証明書rotationの前後を問わず、署名結果から得た実証値（`codesign -dvv`の`TeamIdentifier=`）と**毎回**照合します。同じTeam IDが継続することを、過去の実績や設定値だけから推測で確定しません。
- 同様に、同じcode-signing identifier（`com.workcairn.cli.*`）が証明書rotation後も維持されることも、実際の署名結果から確認します。設定ファイル上の値が変わっていないことをもって、rotation後のdesignated requirementが安定していると仮定しません。
- 証明書rotationを跨いだupgrade acceptance（本ADR「9」のbuild N・N+1が異なる証明書で署名された場合にKeychain継続アクセスが維持されるか）は、実際にrotationを経験したbuild同士で実証されるまで**未確認事項**のままとします（既存の「Unconfirmed hypotheses」の「証明書のrenewal／rotationを跨いだ場合...」がこの事項を指します）。

**既存artifactの有効性 — expiry／rotation／certificate revocation／ticket revocationの区別**:

次の4つは別の事象であり、混同しません。

1. **certificate expiry**（証明書の通常の有効期限切れ）
2. **certificate renewal／rotation**（Humanが能動的に新しい証明書へ切り替えること）
3. **certificate revocation**（Appleが証明書を無効化すること。Developer ID Application証明書はApple Developer Programアカウントの通常操作からは失効できず、`product-security@apple.com`への申請が必要——[Revoking privileges](https://developer.apple.com/help/account/reference/revoking-privileges)）
4. **notarization ticket revocation**（Appleがnotarizationのticket自体を無効化すること。署名鍵が漏洩した場合等にAppleと協力してticketを無効化できるとApple公式資料に記載がある——[Notarizing macOS software before distribution](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)）

通常のrenewal／rotationだけを理由に、過去に署名・notarize・staple済みのartifactを自動的に再署名することはしません。Apple公式資料（[Developer ID](https://developer.apple.com/support/developer-id/)）によれば、Developer ID provisioning profileを使わないソフトウェア（WorkCairnの3 CLI binaryはCloudKit等の advanced capabilityを使わずprovisioning profileも使わないため、この区分に該当します）では、「compileした時点で証明書が有効であった限り、その後証明書が期限切れになっても利用者はdownload・実行できる」とされています。**ただし**、同じApple公式資料は「revokeされた証明書で署名されたDeveloper ID appは、既にinstall済みであってもinstallまたは起動ができなくなる」と明記しています。したがって、既存artifactが将来も無条件に有効であり続けるとは保証しません。既存artifactの有効性は、その時点のApple certificate expiration policy、certificate revocation、notarization ticket revocation、および現在のGatekeeper policyに従います——本ADRはこれらのApple側の挙動そのものを変更・保証する立場にありません。

これに対し、**将来生成するartifact**（rotation後に新たにbuildするDMGやbinary）は、その時点でHumanが明示した有効な新しい`RELEASE_SIGNING_IDENTITY_SHA1`で署名します。「過去に署名済みのartifactの現在の有効性」と「これから何を使って署名するか」は独立した問題として維持し、混同しません。

revocation（certificate revocationまたはnotarization ticket revocation）が発生した場合、またはその疑いがある場合、自動再署名、自動再notarization、既存artifactの自動撤回、別certificateへの自動fallbackのいずれも行いません。release ownerへ報告し、安全に停止して人間の判断を待ちます。

**development／test buildとrelease buildの分離**: `make go-build`、`make v1-release-gate`内のbuild matrix（`scripts/check-release-matrix.sh`）、通常のGo test buildは、本ADR適用後も引き続きad-hoc署名のまま変更しません。署名必須化は`scripts/package-release.sh`のdarwin経路（`make release-package`）だけに適用します。

Humanの証明書private keyのexport・repository保存は採用しません。private keyは通常のApple Developer Programの証明書発行フロー（Xcodeまたは`Keychain Access.app`によるCSR発行・証明書install）でHuman自身のmacOS login Keychainへ留まり、WorkCairnのscript／AIは`codesign -s <SHA1>`でfingerprint参照するだけで、private key material自体へは一切アクセスしません。

### 2. Canonical identifiers

nonbundled executable（bundle構造を持たないCLI binary）は`codesign -i <identifier>`による明示的なcode-signing identifierを必要とします（bundled codeと異なりbundle IDによる既定値がないため）。

比較した候補:

| 候補 | 例 | 採否 |
|---|---|---|
| 既存Keychain service識別子をそのまま再利用 | `com.workcairn.provider.anthropic` | 却下。credential itemの識別子とbinaryのcode identityという別責務を混同し、将来Keychain serviceを調べる開発者がcode-signing identifierと誤認するリスクがある |
| GitHub／Go module path由来 | `github.com.akirashimizu0.workcairn.workcairn` | 却下。ADR-0068で一度repository大文字小文字を修正した実績があり、macOSのsecurity identityという長期安定性が必要な値をrepository slugへ結合すべきではない |
| namespaceなしのbinary名のみ | `workcairn`、`workcairn-daemon`、`workcairn-core` | 却下。reverse-DNS形式のnamespaceを持たず、他の無関係なtoolとの衝突を排除できない。Apple公式例（`com.example.flying-animals.pig-jato`）ともconventionが異なる |
| 新しい専用namespace | `com.workcairn.cli.<binary>` | **採用** |

**Decision**: 3 binaryのcanonical code-signing identifierを次に固定します。

- `workcairn`: `com.workcairn.cli.workcairn`
- `workcairn-daemon`: `com.workcairn.cli.workcairn-daemon`
- `workcairn-core`: `com.workcairn.cli.workcairn-core`

いずれもversion、commit、CDHashを含まない、binaryごとに一意で将来buildでも不変な文字列です。既存のKeychain service `com.workcairn.provider.anthropic`とは`provider.*`／`cli.*`という異なるsecond path segmentで明確に区別します。これらの識別子は登録済みdomainである必要はありません（Apple公式example自体が`com.example.*`という非実在domainを例示している）——WorkCairnの商標／domain確認状況（Public Release Checklist §1、未完了）とは独立した、単なる安定した不透明文字列です。

DMG container自体もApple公式手順上、別途独自の署名とcode-signing identifierを必要とします（後述「6. Canonical Release sequence」）。そのcontainer用識別子として`com.workcairn.dist.macos`を追加で固定します。3 binaryのcanonical identifierとは別の第4の識別子です。

いずれの識別子も、署名後は`RELEASE_EXPECTED_TEAM_ID`（本ADR「1」）と組み合わせて、期待されるdesignated requirementの構成要素になります（identifierだけでなくTeam IDも一致して初めて、DRが安定したidentityとして機能します）。

### 3. Designated requirement / upgrade identity

Developer ID証明書とstable identifierによる署名は、証明書チェーンとidentifierを含む決定的なdesignated requirement（DR）をmacOSに計算させます。ad-hoc署名は証明書チェーンを持たないため、macOSはCDHash（build固有のcontent hash）へ直接依存したDRへfall backします——これがPB-3kで観測した「rebuildごとにDRが変わる」現象の設計上の説明です。Developer ID + stable identifierへ切り替えることで、DRは（証明書が同一である限り）rebuildを跨いで安定するはずだというのが本ADRの設計根拠ですが、これは設計上の期待であり、PB-3jの直接原因の確定を主張するものではありません（「Unconfirmed hypotheses」参照）。

ただし次はいずれも未検証のまま残します。

- 実際に生成されるDRの正確な内容（`codesign -d -r- <binary>`で確認可能ですが、実Developer ID署名binaryが存在しない現時点では確認不能）。
- 証明書のrenewal／rotationを跨いだ場合に、既存Keychain項目へのアクセスがそのまま維持されるか。
- PB-3jの直接原因が実際にこのDR不安定性だったか。

将来のAcceptance（本ADR「9. Upgrade Keychain Acceptance」）では、build Nとbuild N+1の`codesign -d -r-`出力を並べて記録し、DRの安定性とKeychain継続アクセスの両方を実証しなければなりません。

### 4. Hardened Runtime and entitlements — Apple要件とWorkCairn方針の分離

**Apple要件**（Apple公式資料が明示する制約）: `com.apple.security.get-task-allow`エンタイトルメントが**値`true`で存在する**ことがnotarization rejectの原因です。Appleの資料は、このkey自体が常に不在でなければならないとは述べていません（例えばplug-inのdebuggingを目的にhost executableがこのentitlementを`Disable Library Validation Entitlement`と共に含める正当なケースがApple資料自体に記載されています）。

**WorkCairn product policy**（Apple要件を満たしつつ、WorkCairn自身が採る、より狭い運用方針）:

- Release entitlementsのallow-listは、本ADR時点では**空**です。
- `get-task-allow` keyそのものをRelease binaryへ一切含めません（値を`false`にして残すのではなく、key自体を持たせません）。これはApple要件を満たすための必要最小の対応であり、Apple要件がkeyの不在自体を要求しているからではありません。
- 他のいかなるentitlementも、必要性が実証されるまで一切追加しません。現在のADR-0044 Keychain Adapterは`SecItemAdd`／`SecItemUpdate`／`SecItemCopyMatching`をWorkCairn自身のprocess（helper含む）の実効userとして呼ぶだけであり、他appとのkeychain共有（`keychain-access-groups`エンタイトルメント、provisioning profileでの認可が必要）を必要としません。generic Keychain item利用のためだけにこのエンタイトルメントを推測で追加することはしません。
- entitlement追加は、実際のnotarization submission logや実運用evidenceが必要性を示した場合に限る、別の個別判断です。先回りした一括追加は行いません。
- Public Beta release用の3 binaryすべてに`codesign --options runtime`（Hardened Runtime）と`--timestamp`（secure timestamp）を必須とします。いずれもnotarization submissionの必須条件です。
- 将来のverifierは「entitlementがempty allow-listと一致すること（＝entitlementなし）」を確認します（本ADR「11. Release verifier requirements」）。
- Security.framework経路（Keychain generic-password API）はエンタイトルメントなしで動作するという前提でAcceptanceを設計します（ADR-0044の既存実装が既にこの前提で動いているため）。

### 5. Distribution container — DMG fixed contract

比較軸ごとの評価:

| Container | notarization対応 | staple | 3 binary+docs収録 | 操作量 | admin権限 | 暗黙install | local-first |
|---|---|---|---|---|---|---|---|
| tar.gz | 非対応（notary serviceが受理しない） | 不可 | 可 | 展開のみ | 不要 | なし | 適合 |
| ZIP | 対応 | 直接不可（中身へ個別staple後に再zip必要） | 可 | 展開のみ | 不要 | なし | 適合 |
| DMG | 対応 | 対応（直接staple可） | 可 | mount→取り出し | 不要 | なし | 適合 |
| PKG | 対応 | 対応 | 可 | Installer app実行 | 必要（既定install先次第） | あり（install先へ書き込み） | 不適合 |
| app bundle化 | 対応（GUI化前提） | 対応 | CLI 3本には不自然 | GUIとしての操作 | 不要 | なし | 将来候補 |

**Decision**: canonical macOS distribution containerを**DMG（disk image）**とします。

- tar.gzはApple notary serviceが受理する3形式（UDIF disk image、署名済みflat installer package、ZIP archive）に含まれず、そのままでは継続不可能です。
- ZIPは署名も直接stapleもできません。WorkCairnの配布物は単一.appではなく3 CLI binary + docsであり、「中身の各itemへstapleしてから再zip」というZIP用回避策は複数binaryへ繰り返し適用する必要があり複雑さが増します。
- PKGはInstaller appによるinstallという副作用と、install先ディレクトリ・uninstall手順の追加設計を必要とします。WorkCairnは既存のCONSTITUTION Article 12（暗黙のmachine固有pathを要求しない）とlocal-first／simple-by-defaultの製品方針上、install副作用を持つcontainerを避けます。**PKGを選ばない判断として記録**します——今後PKGが必要になる場合（例: 将来のGUI版でLaunchAgent登録等が要る場合）は、install先、uninstall手順、admin権限要否を別ADRで設計する必要があります。
- DMGは署名・staple・checksum・allow-list検証のいずれにも対応し、admin権限も暗黙installも不要で、単一fileとしてGitHub Release assetにも収まり、将来のGUI app bundle配布への自然な移行経路も残します。

**DMG固定contract**（将来実装が満たすべき構造）:

- format: UDIF、compressed read-only `UDZO`（`hdiutil create -format UDZO`相当）。書き込み可能formatは使いません。
- single partition／single volume。複数partition／複数volumeを含む複雑なDMGは作りません。
- canonical volume name: **`workcairn_<version>_darwin_<arch>`**（package rootと同一文字列）を採用します。version・archはRELEASE_VERSION等の既存character class（英数字・`.`・`_`・`-`のみ）に既に制約されているため、macOSのvolume nameが禁止する`:`を含まず、shell／path安全です。同一versionのbuild NとN+1を検証時に同時mountする場合は、既存の「fresh temporary mount directoryを使い、常にdetachする」という検証contract（後述）により、先にmountしたvolumeをdetachしてから次をmountする運用で衝突を避けます——volume nameへ追加のbuild識別子（commit hash等）を含めることはしません（canonical identifierと同様、build固有値を持たせない方針に揃えるためです）。
- canonical package root layout: DMG内は**exactly 1つ**のWorkCairn package rootだけを持ちます。
  - package root名: `workcairn_<version>_darwin_<arch>`
  - `bin/`（3 binaryを格納）
  - `bin/workcairn`、`bin/workcairn-daemon`、`bin/workcairn-core`
  - `VERSION`、`LICENSE`、`README.md`、`CHANGELOG.md`、`SECURITY.md`、`CONTRIBUTING.md`
  - `docs/`
  - `docs/adr/`
- 含めないもの: `.DS_Store`、Finder alias、Finder background画像、hidden installer、LaunchAgent／LaunchDaemon plist。
- symlink、hardlinkを一切含めません。
- 追加のpartitionを含めません。
- implicit install（DMG自体がどこかへ自動コピーする仕組み）を持ちません——利用者がFinderでmountし、bin配下の3 binaryを任意の場所へ手動で取り出す、または直接実行する既存の利用体験を維持します。

**Verifier mount contract**（将来実装のcheck処理が満たすべき手順、具体的なcommand実装は後続Checkpoint）:

- 検証のたびに、fresh（新規・使い捨て）なtemporary mount directoryを作成して使う。既存のmountや自動選択されたmountpoint（`/Volumes/<name>`の既定挙動）を使わない。
- read-only・no browse（Finder browseウィンドウを開かせない、`hdiutil attach -readonly -nobrowse`相当）でmountする。
- mount成功を明示的に確認する。
- mountされたpackage rootへ、本節のallow-list（file一覧、階層構造）を適用する。
- mountで得られたdevice identifier（`/dev/diskN`相当）を取得・記録する。
- 検証がsuccessで終わっても、failureで終わっても、signal（timeout、割り込み）で中断されても、**必ずdetach（`hdiutil detach`相当）する**。
- detach（cleanup）自体が失敗した場合、それを隠さず明示的なerrorとして報告する。
- 「path traversal」というtar archive固有の語彙だけでなく、DMG内のunexpected entry（allow-list外のfile／directory）、symlink、hardlink、想定外の複数volumeの存在を検査する。

**Migration**:

- macOS用archive filenameを`workcairn_<version>_darwin_<arch>.dmg`（+隣接`.sha256`）へ変更します（現行`.tar.gz`から）。
- Linux向け将来archiveは署名・notarizationの対象外であり、既存の`workcairn_<version>_linux_<arch>.tar.gz`形式を変更しません。macOSとLinuxのcontainer形式は本ADR以降、意図的に分岐します。
- checksumは、署名・notarization・staple完了後の最終配布DMG bytesに対して生成します（署名前の中間成果物へは生成しません、詳細は「12. Reproducibility」）。
- `docs/PublicBetaQuickstart.md`、`docs/ReleaseNotes.md`、`README.md`／`README.ja.md`、`SECURITY.md`は、本Checkpointでは意図的に更新しません。これらは実装済みでない配布手順を一般利用者向けに先掲載しないという既存方針（ADR-0070と同様の判断パターン）に従い、実際にDMG生成・署名・notarization実装が完了してから同期します。
- 現行tar.gz candidate（`fa05fc3`のsample archive、`d233b2d`／`ec2940be`世代のarchive）はいずれもhistorical evidenceとしてのみ扱い、署名・notarization実装後の新HEADから生成する最終release assetの代替にはしません。

### 6. Canonical Release sequence

現在の設計（PB-3n.1で報告されたP1）は「tag対象commitからbuildする」としながら、tag作成自体をHuman Acceptance完了後へ位置づけており、矛盾していました。次のように修正します。

**必須境界**:

- Apple notarization自体はtag作成を要求しません（Apple notary serviceはcommit historyやgit tagの存在を一切検証しません）。しかし、**WorkCairnのprovenance契約はlocal tagの先行作成を要求します**——「どのcommitが実際にrelease candidateとしてbuildされたか」をApple notarizationとは独立してWorkCairn自身のgit historyへ固定するためです。
- local tag作成とremote tag push（`git push origin <tag>`）を明確に分離します。
- tag作成、tag push、GitHub Release作成は、それぞれ**別のHuman authorization**を必要とします。1回の承認が3つすべてを兼ねることはありません。
- 生成するartifact（3 binary、DMG）はtag対象commitからのみbuildします。
- Human Acceptance（New-user／Upgrade／Gatekeeper／Provider）完了後、source（tag対象commit）を変更しません。
- Acceptance失敗時、local tagを独断で削除・移動しません（Human判断を待ちます）。
- force update（`git tag -f`、`git push --force`）は行いません。
- tag push（remote公開）は、Human Acceptance完了前には行いません。
- GitHub Release作成は、Human Acceptance完了前には行いません。

**Canonical順序**:

1. final candidate HEADを確定する（既存のtag対象commit決定プロセス、Public Release Checklist §9）。
2. working tree clean、HEAD一致、`VERSION`ファイルの内容確認を行う。
3. Release ownerがlocal tag作成を明示承認する（tag作成専用の承認、build／push／Release承認とは別）。
4. local annotated tag（`git tag -a <tag> -m ...`）を作成する。signed tag（`git tag -s`）を使う場合はDeveloper ID証明書とは別のGPG鍵の話であり、本ADRの範囲外。
5. tag name、tag target commit（`git rev-list -n 1 <tag>`）、`VERSION`ファイルの内容が一致することを確認する。
6. **tag対象commitから**3 binaryをbuildする（native macOS build host、`CGO_ENABLED=1`）。
7. 3 binaryそれぞれを、`com.workcairn.cli.*`固有identifier、`RELEASE_SIGNING_IDENTITY_SHA1`で指定したDeveloper ID Application identity、`--options runtime`、`--timestamp`で個別に署名する。
8. `hdiutil create`でcanonical DMG（本ADR「5」のcontract）を生成し、`com.workcairn.dist.macos` identifierと同じDeveloper ID Application identityでDMG自体を署名する。
9. `xcrun notarytool submit <dmg> --keychain-profile <profile> --wait --timeout 90m`でnotarization submitし、native timeoutだけを使ってclient-side wait上限90分を課し、Acceptedを確認した場合だけ次のstepへ進みます。timeout発生時は自動でstapleへ進まず、自動resubmit・別profileへのfallbackもしません（詳細なtimeout failure処理は本ADR「7」参照）。
10. accepted結果に対し`xcrun stapler staple <dmg>`し、staple／notarization validationを行う。
11. DMGとmount後の3 CLIそれぞれへGatekeeper検証を行う（2層、本ADR「10」参照）。
12. content allow-list、build metadata（version／commit／build date）、checksum（staple後の最終bytes）を検証する。
13. Human Acceptance（New-user Keychain、Upgrade Keychain、Gatekeeper／download、Provider）を実施する（本ADR「8」「9」「10」）。
14. Release ownerが、tag push（remote公開）とGitHub Release作成をこの時点で**別途明示承認**する（step 3の承認とは別物）。
15. tag pushを実行する。
16. GitHub Release を作成し、final DMGと隣接checksumをuploadする。

この順序は、ADR、Public Release Checklist、First-run Acceptance、ROADMAPの記述で統一します。

### 7. Notarization submission — credential handling／bounded wait／evidence contract

**Credential handling**（本ADR「1」の3変数contractのうち`RELEASE_NOTARY_PROFILE`を使用）:

- `notarytool` Keychain profileだけを標準workflowとします。profile名（非secret参照）だけをrelease scriptへ渡し、`--keychain-profile <name>`としてそのまま使います。
- Apple ID、app-specific password、API key本体をargv、環境変数、log、`.env`、repositoryへ渡す・保存することはありません。
- profile未設定の場合はfail-closedで停止し、別のcredential種別へ自動fallbackしません。
- submission rejectionやtimeoutに対する自動resubmissionは行いません。

**Bounded wait contract**:

- `notarytool submit --wait`を無制限に待ちません。client側のwait／polling上限（deadline）は**90分**です。Apple公式資料は通常のscan処理を「usually less than an hour」（Xcode自動notarizationの説明）、また大半（98%）は15分以内に完了するとしています。60分ちょうどをdeadlineにすると、Appleの言う「通常はこれ未満」という目安と実質的な余裕（margin）がなく、病的に遅いケースでなくても偽timeoutを頻発させるリスクがあります。90分は、Appleの通常ケース上限へ約1.5倍の安全マージンを確保しつつ、依然として明確に有限（無制限ではない）で、Human Operatorがrelease作業中に無期限に待たされることもない値として選びました。**この90分はclient側のwaitをどこまで許すかの上限であり、Apple側のnotarization job処理そのものを90分でcancelさせる保証ではありません**。client側がtimeoutした後も、Apple側のsubmissionは引き続き処理が継続している可能性があります。
- canonical contract: release hostの`notarytool`は、`submit --wait --timeout`を正式にサポートするversion／capabilityを**必須条件**とします。**canonical invocationは`notarytool submit ... --wait --timeout 90m`に固定します。** `90m`は、対象local `notarytool 1.1.2 (41)`の`submit --help`出力（"'duration' is an integer followed by an optional suffix: seconds 's' (default), minutes 'm', hours 'h'. Examples: '3600', '60m', '1h'"）で確認済みの、整数＋単一suffix（`m`＝minutes）というduration syntaxに正確に一致するcanonical valueです。`1h30m`のような複合suffix形式は、この確認済みhelp出力のexampleには含まれておらず、documented formとしては扱いません。Apple公式の[Customizing the Xcode archive process](https://developer.apple.com/documentation/security/customizing-the-xcode-archive-process)が例示する`--wait --timeout 2h`は、native duration optionが実在することの根拠として引き続き参照しますが、WorkCairnのproduct policy値は90分（`90m`）です。
- 実装は、release scriptがversion文字列を推測するだけで`--wait --timeout`の対応を判断しません。release preflightで`notarytool submit --help`相当の出力から、必要option（`--wait`、`--timeout`）の存在を確認します。
- `--wait`または`--timeout`のいずれかが利用できないrelease hostでは、release packagingをfail-closedで停止します。WorkCairnが所有するGo製のrelease helper／process supervisorへのfallbackや、標準macOS環境で保証されないGNU `timeout`（coreutils）・Homebrew版`timeout`への切り替えは行いません。Python、Node、Homebrew、GNU coreutilsのような追加dependencyもrelease packagingへ導入しません。
- timeoutを理由に新しいsubmissionを自動作成しません。
- `RELEASE_NOTARY_PROFILE`が指す1つのprofile以外へのfallbackを行いません。
- timeout（`notarytool`自体がclient側timeoutとして終了する場合を含む）発生時、同じsubmission IDのstatusを確認できた場合だけ、そのIDに対して`xcrun notarytool info <submission-id> --keychain-profile <profile>`相当のstatus確認を**1回だけ**行います。
- **`notarytool`がclient側timeout時に必ずsubmission IDを返す（または出力する）かどうかは、本ADR時点では確定済みのApple公式契約として扱いません。** 閲覧したApple公式資料は、通常の`submit`成功時にsubmission IDが出力されることは示していますが、`--timeout`到達時に何が出力されるかまでは明示していません。この点は実装Checkpointが、(a) 対象`notarytool`のhelp／versionからの確認、(b) fake-tool test、(c) 実際のnotarization実行時に得られるsanitized evidence、の3つによって特性を明らかにする**未検証事項**として扱います。
- submission IDを取得できた場合（timeout到達時の出力から、またはsubmit直後の出力から確認できた場合）だけ、そのIDに対する状態確認へ進みます。
- submission IDを取得できなかった場合は、status確認不能として安全に停止します。re-submit（新しいsubmissionの自動作成）はしません。
- status確認の結果が`Accepted`であっても、timeout eventが発生した場合は自動的に後続step（staple等）へ継続しません——timeout eventとその時点のstatusをcontroller（Human）へ報告し、Human判断を待ちます。
- raw notarization output（`notarytool submit`の生標準出力／標準エラー全体）、credential、profile secretをrepository、chat、release evidenceへ保存しません。保存してよいのは下記「Evidence handling」のsanitized failure evidenceだけです。

**Evidence handling**:

- submission IDはcredentialではありませんが、公開docs（README等）へ無条件掲載しません。
- raw notary log（`notarytool log`の生JSON出力）をrepository、GitHub Release、AIとのchatへ貼り付けません。
- evidence summaryは次のallow-listされたfieldだけに限定します。
  - submission ID
  - status（`Accepted`／`Invalid`等）
  - submitted／completed timestamp
  - issue件数
  - normalized package-relative issue path（例: `bin/workcairn-daemon`——絶対pathではなくpackage root相対）
  - safe issue code／message（Appleが返す定型的なmessage文字列。username、home directory等の実行環境固有情報を含まないことを確認してから記録）
- username、home directory path、Apple ID、credential、local絶対pathをevidence summaryから除去します。
- rejected／timeoutになったsubmissionの中間artifact（未staple DMG等）は、final output directoryとは分離した場所へ置きます。
- これらのfailed-evidence artifactへfinalなfilename・checksumを付与しません（final release assetと誤認させないため）。
- 自動削除はせず、明示的な`failed-evidence`ディレクトリ（final output directoryの外）へ保持します——原因調査のための証跡として残しますが、final assetとは物理的にも命名的にも区別します。

### 8. New-user Keychain Acceptance（signed build N、未実装 — signing実装後に実施）

現在のraw CLI配布物（`.app`ではない3つのCLI binary）に対し、「ダブルクリック起動」や特定の「開く」ダイアログを要求する表現は実態と合わないため、Finderで**DMGをopenし**、Terminalから**raw CLIを実行する**という実際の操作へ揃えます。

1. clean macOS userまたは明示隔離環境を用意する。
2. quarantine属性が付いた状態で、canonical Release asset（署名・notarization・staple済みDMG）を実際のdownload経路（ブラウザ等）から取得する。
3. Finderで通常のdouble-click open（DMGファイル自体をopenする、既存のGatekeeper確認ダイアログが出ればHumanが確認して進める）でmountする——`xattr`削除や右クリックoverrideを使わない。
4. Terminalから、mountされたvolume配下の3 binaryそれぞれの`version`出力を確認する。
5. macOS native hidden-inputでtest用credentialをWorkCairn Keychain item（`com.workcairn.provider.anthropic`）へ保存する（`workcairn-daemon`経由）。
6. 同じbuild Nでread-backが成功することを確認する。
7. graceful shutdownする。
8. 同じbuild N（同一bytes）を再起動する。
9. Keychainを再入力せずcredentialが読めることを確認する。
10. credentialが値・一部・長さ・fingerprintのいずれの形でも露出しないことを確認する。
11. test credentialを失効またはrotationする（本Acceptance専用のtest credentialであり、他のAcceptanceとは共有しない。「9」節Credential separation参照）。
12. Acceptance evidence（署名identity SHA-1、Team ID、notarization submission ID、DMG／3 CLIそれぞれのGatekeeper検証結果、各step結果）を保持する。

### 9. Upgrade Keychain Acceptance（signed build N → signed build N+1、未実装）

**Build N**:

- immutable commitまたはlocal annotated test tagへ拘束する。
- source commit SHAを記録する。
- version（`VERSION`ファイルの内容）を記録する。
- build date（build metadata）を記録する。
- `workcairn-daemon`binaryのSHA-256／CDHashを記録する。
- Developer ID identityのSHA-1 metadataを記録する。
- 実Team IDを記録する。
- `workcairn-daemon`のcanonical identifier（`com.workcairn.cli.workcairn-daemon`）を記録する。
- `codesign -d -r-`によるdesignated requirementを記録する。

**Build N+1**:

- Nとは異なるimmutable commitまたはtagへ拘束する。
- source commit SHAがNと異なる。
- embedded commit metadata（buildinfo）がNと異なる。
- `workcairn-daemon`のbinary bytesがNと異なる。
- `workcairn-daemon`のSHA-256またはCDHashがNと異なる。
- `RELEASE_EXPECTED_TEAM_ID`はNと**同じ**。
- `workcairn-daemon`のcanonical identifierはNと**同じ**（`com.workcairn.cli.workcairn-daemon`のまま）。
- `codesign -d -r-`によるdesignated requirementを記録する（Nの記録と比較する対象）。

**Keychain test対象**: Keychain credential resolveを行う`workcairn-daemon`だけを対象とします（`workcairn`／`workcairn-core`はKeychainを読まないため対象外）。

- build NでKeychain経由でtest credentialを登録する。
- build N再起動でread成功を確認する。
- Keychain項目を変更しないまま、daemon binaryだけをbuild N+1へ置換する。
- build N+1で、credentialを再入力せずreadが成功することを確認する。
- system prompt（macOSのKeychain access許可ダイアログの再表示等）や、暗黙のmigration機構へ依存しない——そのような機構は存在しない前提で手順を組む。
- failure時はcredentialを変更しない。
- retry／別sourceへのfallbackをしない。
- 既存のsafe diagnostic（ADR-0066／PB-3m／PB-3m.2のclosed classification、`SafeCredentialSubstage()`／`SafeCredentialCategory()`）をそのまま記録する。

**Distribution upgrade**（container自体の検証、Keychain testとは別軸）:

- N・N+1ともDeveloper ID署名する。
- N・N+1ともnotarizeする。
- N・N+1ともstapleする。
- N・N+1ともcanonical DMG化する。
- DMG全体のupgrade取得（新しいDMGを別途download）とmountも確認する。
- Gatekeeper verification（DMG層・3 CLI層の両方、本ADR「10」）をN・N+1両方で実施する。

**Credential separation**:

- New-user Acceptance（本ADR「8」、以下「C」）とUpgrade Acceptance（本節、以下「D」）は、**別のtest credential・別のsession**を使用します。
- Cで使ったtest credentialは、Cの手順終了時点で失効可能とします（Dの手順が始まる前に失効してよい）。
- DはD専用のtest credentialをbuild Nで登録し、build N+1でのread完了を確認した後に失効します。
- Provider Acceptance（Plan生成等の実Provider呼び出しを伴う確認）を別途行う場合も、そのcredentialのlifecycle（登録・使用・失効のタイミング）を明示的に記録します。C・D・Provider Acceptanceのいずれでも、Humanの既存real Anthropic credentialは使用しません。
- Acceptance evidence／記録データ自体は（credential値そのものを除き）削除せず保持します。

現在のad-hoc buildで作成された既存Keychain項目は、このAcceptanceの起点として使いません（build Nから新規に作り直します）——ad-hoc署名時代のitemをstable signed releaseのupgrade証明へ流用すると、何を検証したのか不明瞭になるためです。

### 10. Gatekeeper / download Acceptance（未実装）— 2層検証

DMG層と内部3 CLI層を分離し、**両方を必須**とします（「または」で結びません）。

**DMG層**:

- `spctl --assess --type open --context context:primary-signature <dmg>`でDMG自体のsignature／notarization／stapleを確認する。
- FinderでDMGを通常のdouble-click openでmountする。
- right-click override不要。
- `xattr`削除不要。

**内部3 CLI層**:

- mount済みDMG内の3 binaryそれぞれに対し`spctl --assess --type exec`を実行する。
- Terminalから3 binaryそれぞれのversion metadata（`version`出力）を確認する。
- 実際にdaemonとして起動するのは`workcairn-daemon`だけである（`workcairn`／`workcairn-core`はCLI呼び出しのみで常駐しない）。
- `workcairn`／`workcairn-core`は、version出力とごく短いsmoke（例: `workcairn-core`のJSON Contract v1呼び出し1回）だけを確認対象とする。
- 特定のGUI dialogが表示されること自体を成功条件にしない（Gatekeeperの確認ダイアログの文言や見た目に依存した判定をしない。「起動が拒否されないこと」を確認する）。
- Gatekeeper rejectがないことを確認する。

**両層共通の禁止事項**:

- macOS全体のSecurity設定の恒久的な緩和を要求しない。
- right-click override（「開発元を確認できないため開けません」からの右クリック起動）を標準手順として案内しない。
- quarantine属性の削除（`xattr -d com.apple.quarantine`）を標準手順として案内しない。
- Gatekeeper rejectをknown limitationとして容認しない——rejectしたら実装または署名／notarizationの誤りとして扱い、修正する。

正式GitHub Release作成前にこのAcceptanceが必要な場合、GitHub Draft Releaseのasset（限定公開）を候補として記録します。ただしDraft Release作成・asset uploadは本ADRの範囲外であり、別途Human承認が必要です（本Checkpoint自体はGitHub操作を一切行いません）。

### 11. Release verifier requirements — evidence分類

将来の`scripts/verify-release-archive.sh`相当（またはDMG専用の新script）が検査すべき項目を、evidenceの性質ごとに5つへ分類します。**最終的なAuthoritative final acceptanceは、これら全区分の組合せであり、いずれか1区分だけで代替することはできません。**

**Offline deterministic**（署名済みasset自体から、ネットワークもHuman操作も不要に決定的に検査できるもの）:

- codesign identity（Developer ID Applicationであること、`RELEASE_SIGNING_IDENTITY_SHA1`と一致すること）
- Team ID（`RELEASE_EXPECTED_TEAM_ID`と全asset間で一致すること）
- code-signing identifier（`com.workcairn.cli.*`／`com.workcairn.dist.macos`との一致）
- Hardened Runtimeが有効であること
- secure timestampが存在すること
- entitlement allow-list（空集合）との一致
- architecture（arm64）
- build metadata（version／commit／build date）の一致
- DMG layout（本ADR「5」のcontract、mount結果に対するallow-list検査）
- symlink／hardlink不在
- checksum（staple後の最終bytesとの一致）

**Apple service evidence**（Apple notary serviceとの通信結果、ネットワーク到達が必要）:

- notary status `Accepted`
- submission ID
- sanitized notary result（本ADR「7」のevidence allow-list）

**Local policy evidence**（ローカルのmacOS policy engineによる評価、ネットワーク不要だがOS状態に依存）:

- `stapler validate`
- DMGに対する`spctl --assess --type open`
- 3 CLIそれぞれに対する`spctl --assess --type exec`

**Human evidence**（自動化できず、Humanの実機操作でしか得られないもの）:

- 実際にquarantine属性が付くbrowser downloadの実施
- Finderでのmount
- Terminalでのversion確認／daemon起動
- override・Security設定の恒久的緩和が一切不要だったことの確認

**Static fake-tool test**（実署名・実notarization・実Keychainを一切使わない、既存のGo test disciplineに沿った単体テスト対象。実装Checkpointが書くべきtestの種類として記録）:

- command構築の順序（本ADR「6」の16 step順）
- 正確なargument構築（`-i`、`-s`、`--options runtime`、`--timestamp`等のflag組み立て）
- notarization submit commandに`--wait --timeout 90m`が**正確に1組**含まれること（`--wait`と`--timeout`のどちらか一方だけの実行可能commandを許容しない）
- `90m`以外の値をcanonical Release commandへ渡さないこと（`1h30m`等の複合suffix形式や他のduration文字列を拒否する）
- `--wait`欠落を拒否すること
- `--timeout`欠落を拒否すること
- fail-closed挙動（SHA-1 0件／複数件、Team ID mismatch、profile未設定、`--wait`／`--timeout`いずれかのcapability不足等）
- identity／Team mismatch検出（証明書rotation後のfingerprint更新漏れを含む）
- （notary log等の）parser
- submission IDのstrict抽出（あいまい部分一致・複数候補推測の禁止）
- timeout時のsubmission ID可用性分岐: **IDありなら同じsubmissionへのstatus確認を1回だけ行う**。**IDなしならstatus確認不能として停止する**（re-submitしない）。
- no resubmit／no retry／no fallback（timeout・rejection・profile fallbackのいずれでも）
- rejectionハンドリング
- cleanup（DMG detach等）の確実な実行
- raw output（`notarytool submit`の生標準出力等）を保存しないこと
- checksum-after-staple（stapleより前にchecksumを計算していないことの検証）

検査不能な状態（例: notarization APIへ到達できない、`codesign`が存在しないhost）を無条件でPASSとして扱いません。検査不能はfail-closedで明示的なerrorとして報告します。

### 12. Reproducibility and provenance

Developer ID署名、secure timestamp、notarizationの追加により、最終binary／DMG bytesは厳密なbit-for-bit reproducible buildではなくなります（secure timestampはApple timestampサーバーが付与する値であり、notarization ticketのstapleはbytesを追加するため）。次を明確に区別します。

- **unsigned build reproducibility**: source commit、build metadata（version／commit／build date、既存`buildinfo`）が同一なら、署名前のGo binary自体は`-trimpath -buildvcs=false`の既存方針の範囲でreproducibleであり続けます（本ADRはこの既存保証を変更しません）。
- **signed final asset provenance**: 署名identity（SHA-1、Team ID含む）、notarization submission ID、staple後の最終checksumという別の来歴情報を記録します。これらはbuildごとに再現される値ではなく、「どのApple account・どのnotarization requestで生成されたか」という証跡です。

notarizationはApple外部への通信を伴う操作です。`go test`、`make v1-release-gate`、`make public-beta-smoke`、`make check-ui-*`、`make public-beta-build-matrix`を含む通常のtest／buildコマンドからは絶対に呼び出しません。将来実装される明示的なrelease command（例: `make release-package RELEASE_GOOS=darwin ...`のsigned/notarizedバリアント）だけがnotarizationをトリガーします。

### 13. Failure policy

次を恒久的に禁止します。

- ad-hoc fallback
- unsigned fallback
- 別のcertificateやTeam IDへの自動fallback
- notarization skip
- 自動resubmission
- rejectionの無視
- 利用者へのGatekeeper override要求（`xattr`削除、右クリックoverrideの標準手順化）
- staleな署名済みartifactの再利用（新HEADのbuildを常に必要とする）
- staple前のchecksumをfinalとして公開すること
- Keychain read failure時のcredential自動再登録
- upgrade failure時のsecret migration
- raw Apple credentialやnotary logの内容（secret部分）を外部へ露出すること
- **tag push（remote公開）をHuman Acceptance完了前に行うこと**
- **GitHub Release作成をHuman Acceptance完了前に行うこと**
- **local tagのforce update（`git tag -f`）**
- **Acceptance失敗時、Human判断を待たずにlocal tagを独断で削除・移動すること**

## Human prerequisites

次はいずれもHuman作業として明示し、本ADR自体では加入済み・証明書存在・profile存在のいずれも仮定しません。

- Apple Developer Program（またはEnterprise Program）への加入状況の確認。
- Account Holder／必要権限の確認。
- Developer ID Application証明書の取得。
- private keyのmacOS Keychainへの保持（通常のcertificate発行フローに従う、export・repository保存はしない）。
- 実Team IDの確認（`RELEASE_EXPECTED_TEAM_ID`として供給）。
- Developer ID Application certificateのSHA-1 fingerprint確認（`RELEASE_SIGNING_IDENTITY_SHA1`として供給）。
- certificateの有効期限・revocation状態の確認。
- `notarytool` Keychain profileの作成（`RELEASE_NOTARY_PROFILE`として名前だけを供給）。profile作成時に使用したTeam IDのevidenceの記録。
- Apple Developer Program利用条件・費用の承認。
- 署名・notarizationに伴う外部（Apple）通信の明示承認。
- GitHub Draft Release assetを使う場合のGitHub変更承認（別途）。
- real signing identity metadata（`security find-identity`の実出力等）をAIへread-only確認させる場合の個別承認。
- local tag作成の承認、tag push／GitHub Release作成の承認（本ADR「6」——それぞれ別の承認）。

このCheckpointでは、加入済み、certificate存在、profile存在を仮定しません。

## Consequences

- Public Beta macOS配布はDeveloper ID署名・Hardened Runtime・notarization・stapleが揃うまで、正式なGatekeeper経路での配布ができません。
- macOS配布containerがtar.gzからDMGへ変わるため、Quickstart／Release Notes／READMEの利用者向け手順は、実装完了時に別Checkpointで同期する必要があります（本Checkpointでは意図的に据え置き）。
- `scripts/package-release.sh`／`scripts/verify-release-archive.sh`は将来の実装Checkpointで拡張が必要です（本Checkpoint時点では未変更）。
- rebuildごとのKeychain ACL不安定性は、Developer ID + stable identifierにより改善される設計上の期待がありますが、実証されるまでPB-3のPublic Beta Acceptanceを再開する根拠にはなりません。
- 新たにApple外部サービス（notarization、secure timestamp server）への依存が、release packaging（テスト・buildではなく）の経路にだけ追加されます。
- Release順序へlocal tag作成が明示的に組み込まれたことで、release candidateのprovenanceがWorkCairn自身のgit historyだけからも常に確認可能になります。

## Rejected alternatives

- **ad-hoc署名のまま維持**: ad-hoc署名build間でKeychain継続アクセス失敗を実際に観測しており（PB-3j）、Gatekeeperの通常経路でも一貫してrejectされるため、Public Beta配布の恒久解決にならないとして却下。CDHash／designated requirementがbuild間で異なることは確認済みですが、これがKeychain失敗のexact causeであるとは断定していません——それでもDeveloper IDはstable identityとGatekeeper通過の両方のために必要と判断しました。
- **unsigned binaryをそのまま配布**: Gatekeeperの通常経路で拒否され、`xattr`削除等の非標準手順を利用者へ要求することになるため却下。
- **PKG installer**: install先・uninstall・admin権限という新しい副作用面を追加し、WorkCairnのlocal-first／no-implicit-install方針に反するため却下（将来GUI版でLaunchAgent等が必要になれば再検討）。
- **ZIP archiveの維持**: 署名も直接stapleもできず、複数binaryへの回避策が複雑になるため却下。
- **Keychain service識別子をcode-signing identifierとして再利用**: 責務混同のため却下。
- **Apple ID + app-specific passwordをscript環境変数へ直接渡す**: cleartext secretをscript実行環境へ露出するため却下し、`notarytool` Keychain profileへ限定。
- **notarization失敗時の自動resubmission**: rejection原因を確認せず繰り返すことは、既存の「known-flakeでも自動retryしない」方針と一貫しないため却下。
- **identity表示名の完全一致を主契約にする**: 表示名は証明書renewal等で変わり得る非decisiveな文字列であり、typo・空白差異にも脆いため却下し、SHA-1 fingerprint + 実署名結果のTeam ID照合へ置き換え。
- **tag作成をHuman Acceptance後に位置づける（PB-3n初版の設計）**: 「tag対象commitからbuildする」という記述と矛盾するため却下し、tag作成をbuildより前・Acceptanceより前の別の明示承認へ位置づけ直した（本ADR「6」）。
- **GNU `timeout`への依存**: 標準macOS環境で保証されないcoreutilsコマンドであり、native `notarytool submit --wait --timeout`が確認できる以上、これに依存する理由がないため却下。
- **Homebrew等の非標準packageへの依存**: release packagingにGo Only以外のpackage managerやruntimeを追加することになり、既存Go Only方針に反するため却下。
- **Go supervisor（自前のprocess timeout実装）へのfallback**: PB-3n.4版ADRで一度採用したが、Apple公式資料とlocal `notarytool`の両方でnative `--wait --timeout`が確認できたため不要と判断し、PB-3n.5aで撤回。native optionが確認できる限り、それを迂回する自前実装は複雑さを増すだけであり却下。
- **native timeout非対応の`notarytool`を黙って受け入れる**: `--wait`／`--timeout`のcapability確認を省き、対応の有無を確認せず進めることは、実際にdeadlineが機能しないままrelease packagingが動いてしまうriskがあるため却下し、release preflightでの明示確認とfail-closedを採用した。

## Implementation checkpoints（本ADRでは未着手、将来候補として記録）

1. `PB-3n.5d Final Architecture Re-review`（Codex独立re-review、本Checkpointの直後）。
2. `scripts/package-release.sh`darwin経路への署名・DMG生成ステップの実装（`RELEASE_SIGNING_IDENTITY_SHA1`／`RELEASE_EXPECTED_TEAM_ID`／`RELEASE_NOTARY_PROFILE`の3変数contractを本ADR通りに実装、証明書rotation時の明示fingerprint更新手順を含む）。
3. local tag作成を含む本ADR「6」の16 step canonical Release sequenceの実装（release scriptとoperator手順の両方）。
4. native `--wait --timeout`対応`notarytool`のrelease preflight実装（`submit --help`相当からの`--wait`／`--timeout`存在確認、非対応hostでのfail-closed。Go supervisor、GNU `timeout`等へのfallbackは持たない）。
5. `notarytool submit ... --wait --timeout 90m`（canonical duration固定済み）によるnotarization submission／staple自動化の実装（evidence allow-list）。
6. `notarytool`のclient側timeout時のsubmission ID可用性characterization——依然として別項目の未検証事項（help／versionからの確認、fake-tool test、実notarization実行時のsanitized evidenceの3種で明らかにする）。
7. `scripts/verify-release-archive.sh`相当の署名・notarization・Gatekeeper検証拡張（本ADR「11」の5分類すべて）。
8. DMG mount contract（fresh temporary mount、read-only、no browse、常時detach）の実装。
9. 実Developer ID署名binaryでのbuild N designated requirement確認（本ADR「3」の未検証事項の解消）。
10. New-user Keychain Acceptance実施（本ADR「8」）。
11. Upgrade Keychain Acceptance実施、build N→N+1のDR比較とcredential separationを含む（本ADR「9」）。
12. Gatekeeper／quarantined download Acceptance実施、DMG層・3 CLI層の両方（本ADR「10」）。
13. Quickstart／Release Notes／README／SECURITY.mdの配布手順同期（DMG化・署名済み配布物への言及）。

## Official Apple references

- [Developer ID](https://developer.apple.com/support/developer-id/)
- [Signing Mac Software with Developer ID](https://developer.apple.com/developer-id/)
- [Notarizing macOS software before distribution](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)
- [Creating distribution-signed code for the Mac](https://developer.apple.com/documentation/xcode/creating-distribution-signed-code-for-the-mac/)
- [Packaging Mac software for distribution](https://developer.apple.com/documentation/xcode/packaging-mac-software-for-distribution)
- [Customizing the notarization workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)
- [Customizing the Xcode archive process](https://developer.apple.com/documentation/security/customizing-the-xcode-archive-process)
- [Resolving common notarization issues](https://developer.apple.com/documentation/security/resolving-common-notarization-issues)
- [Revoking privileges](https://developer.apple.com/help/account/reference/revoking-privileges)
