# Public Beta macOS First-run Acceptance

この手順が実Keychain、実Providerを使う人間確認です。自動testはtemporary directoryとFakeだけを使い、既存の個人Obsidian Vaultには触れません。

**公開後の現在状態（PHASE PB-3co）**: `v1.0.0-beta.1`は、署名・notarization・staple済みのmacOS／arm64 DMGとしてGitHub prereleaseへ公開済みです。次の必須Acceptanceは、tag対象candidateとは別に保持したcredential非包含のsanitized evidenceによってすべてCOMPLETE／PASSと確認されました。

- セクションC（signed build New-user Keychain Acceptance）: PASS
- セクションD（signed build Upgrade Keychain Acceptance）: PASS
- セクションE（quarantined download／Gatekeeper Acceptance）: PASS
- bounded Provider Acceptance（Plan 1回、Task 1件、Review 1回、Revision／Recovery／retry／fallbackなし）: PASS
- PB-3cm public asset post-publish verification: PASS
- PB-3cn Safari download／quarantine／Finder mount／Gatekeeper smoke: PASS

tag対象sourceと配布candidate内に残るrelease前のNO-GO文言は、candidateを凍結した時点のhistorical snapshotです。公開後の完了状態は上記の外部sanitized evidenceと本PHASE PB-3co記録で追補し、既存tag、Release、assetは変更しません。今後の機能追加や再Acceptanceは次versionの新しいcandidateを対象とし、`v1.0.0-beta.1` candidateを再利用しません。

以下ではセクションAをad-hoc署名binary時代のhistorical product-flow evidenceとして保持し、C／D／Eは将来candidateでも再実施する正式手順として残します。

## A. Historical — ad-hoc署名binaryによるproduct-flow evidence（Public Beta GOの根拠にならない）

必須手順はMacだけで完結します。iPhone、`--local-network`、iCloud Drive、Obsidianはいずれも不要です。**この手順はTeam IDなし・rebuildごとにdesignated requirementが変わるad-hoc署名binaryに対するものであり、単一buildの起動・Keychain登録・再起動継続だけを検証します。upgrade（build N→N+1）を跨いだKeychain継続アクセスはこの手順では検証できません（PB-3jで実際に破綻）。**

1. final tag対象commitから作ったdarwin／arm64 packaged binaryのarchiveと隣接checksumを確認し、clean directoryへ展開する。
2. 3 binaryの`version`出力でversion metadataとcommit metadataを確認する。
3. `workcairn-daemon`を既定loopbackで起動する（`--local-network`は使用しない）。
4. 新規install相当の状態で、native folder pickerが自動表示されることを確認する。
5. 空の`WorkCairn`専用folderを新規作成して選ぶ。ローカルfolderだけで完結し、iCloud Driveを要求しない。iCloud rootや既存の個人Vaultは選ばない。
6. credential登録前に、Provider必須の操作が安全に拒否されることを確認する。
7. Macに自動表示されたFirst-run Wizardで「最初のAIチームを作ります」を確認し、明示承認する。
8. Product Manager、Content Writer、QA EngineerがAI社員一覧へ表示されることを確認する。
9. `AI Connections`で`MacでClaudeを接続`を押し、Macのnative hidden-inputへtest用credentialを入力し、Keychainへ登録する。
10. Claudeが`Connected`、Routingが`Automatic`となり、Model ID入力がないことを確認する。
11. `会社を始める`を押し、最初の自然言語依頼画面へ移ることを確認する。
12. 依頼を1件送り、「このように進めます」でPlanを確認する。通常表示に`PROPOSED-*`、Role ID、digestが出ないことを確認する。
13. Maker実行、別のQA ReviewerによるReviewまで進める。必要ならRevisionはそのまま確認してよいが、Plan 1回、Task 1件、Review 1回という最小構成を意図的に増やさない。
14. 自動retryや別Provider fallbackが起きないことを確認する。
15. 実行中は小さい「会社が働いています」だけ、質問／承認／failure時だけ依頼詳細が前面に出ることを確認する。
16. Timeline、Proof of Work、Deliverable、Reviewを確認する。failureを起こした場合は画面移動／reload後も消えず、technical detailsをcopyできることを確認する。
17. daemonをgraceful shutdownして再起動する。folder pickerを再表示せず、同じ専用folderと過去のSession、Timeline、Plan、Task、Deliverable、Review、Revision、failure／Recovery evidenceを表示することを確認する。
18. credentialがbrowser、HTTP payload、Vault、Command Ledger、Audit、log、shell history、screenshotへ出ていないことを確認する。
19. test後にcredentialを失効またはrotationする。
20. 同一Vaultへ複数daemonをwriterとして起動しない。

**上記はProvider Plan／Task／Reviewというproduct flow自体がad-hoc署名buildで動作したことを示すhistorical evidenceであり、Public Beta GOの判断根拠にはなりません。** signed buildに対するProvider Acceptance、およびセクションC／D／Eが別途必須です。

## B. 任意確認

次はいずれもPublic Beta GOの前提ではありません。実施しなくてもPublic BetaをGOにできます。既存の個人Vaultは必須Acceptanceに使用しません。

- iPhone／iPad、`--local-network`、pairing
- iCloud Drive
- Obsidian（Macで`Obsidianで見る準備`を押し、`Open folder as vault`で専用folderを開く）
- 複数Mac／VM
- 実Vaultのcopy、migration
- native filesystemでのCAS conflict追加stress
- 実Vaultのbackup／restore演習

## C. 署名済みbuild — New-user Keychain Acceptance（[ADR-0071](adr/ADR-0071-macos-developer-id-signing-and-notarization.md)「8」、`v1.0.0-beta.1`: COMPLETE／PASS）

[Manual macOS Signed Release Procedure](ManualMacOSReleaseProcedure.md)に沿ってHumanが生成したsigned build Nに対する手順です。`v1.0.0-beta.1`では新規final candidateに対して初回登録、read-back、同一build再起動後の再入力なしread-backを確認しました。PB-3u.2〜7のdiagnostic candidateはこのAcceptanceへ再利用していません。automation実装の有無はこの条件の代替にも免除事由にもなりません。`.app`ではないraw CLIであるため、「ダブルクリック起動」や特定の「開く」ダイアログではなく、**FinderでDMGをopenし、Terminalからraw CLIを実行する**という実際の操作へ手順を揃えます。

1. clean macOS userまたは明示隔離環境を用意する。
2. quarantine属性が付いた状態で、canonical Release asset（署名・notarization・staple済みDMG）を実際のdownload経路（ブラウザ等）から取得する。
3. FinderでDMGファイル自体を通常のdouble-click openでmountする——`xattr`削除や右クリックoverrideを使わない。
4. Terminalから、mountされたvolume配下の3 binaryそれぞれの`version`出力を確認する。
5. macOS native hidden-inputでtest用credentialをWorkCairn Keychain item（`com.workcairn.provider.anthropic`）へ保存する（`workcairn-daemon`経由）。
6. 同じbuild Nでread-backが成功することを確認する。
7. graceful shutdownする。
8. 同じbuild N（同一bytes）を再起動する。
9. Keychainを再入力せずcredentialが読めることを確認する。
10. credentialが値・一部・長さ・fingerprintのいずれの形でも露出しないことを確認する。
11. 本Acceptance（C）専用のtest credentialを失効またはrotationする（セクションDとは別のcredential・別のsessionを使う。Dの手順が始まる前に失効してよい）。
12. Acceptance evidence（署名identity SHA-1、Team ID、notarization submission ID、DMG／3 CLIそれぞれのGatekeeper検証結果、各step結果）を保持する。

## D. 署名済みbuild — Upgrade Keychain Acceptance（[ADR-0071](adr/ADR-0071-macos-developer-id-signing-and-notarization.md)「9」、`v1.0.0-beta.1`: COMPLETE／PASS）

signed build N→signed build N+1のKeychain継続アクセスを検証する手順です。`v1.0.0-beta.1`では、別々に生成・署名・notarize・stapleしたN／N+1を用い、同じtest credentialを再入力せずにN+1からreadできることを確認しました。PB-3jはad-hoc署名buildでこの継続アクセスが破綻したことを観測しており（causalityは未確定、[ADR-0071](adr/ADR-0071-macos-developer-id-signing-and-notarization.md)「Unconfirmed hypotheses」参照）、将来candidateでも本手順を省略しません。

**Build N**: immutable commitまたはlocal annotated test tagへ拘束し、source commit SHA、version、build date、`workcairn-daemon`のSHA-256／CDHash、Developer ID identityのSHA-1、実Team ID、canonical identifier、`codesign -d -r-`のdesignated requirementを記録する。

**Build N+1**: Nとは異なるimmutable commitまたはtagへ拘束し、source commit SHA、embedded commit metadata、daemon binary bytes、daemon SHA-256／CDHashがNと異なることを確認する。`RELEASE_EXPECTED_TEAM_ID`とcanonical identifierはNと同じであることを確認し、designated requirementを記録してNの記録と比較する。

**Keychain test**（`workcairn-daemon`のみが対象。Keychainを読まない`workcairn`／`workcairn-core`は対象外）:

1. build NでKeychain経由でtest credentialを登録する。
2. build N再起動でread成功を確認する。
3. Keychain項目を変更しないまま、daemon binaryだけをbuild N+1へ置換する。
4. build N+1で、credentialを再入力せずreadが成功することを確認する。
5. system prompt（Keychain access許可ダイアログの再表示等）や暗黙のmigration機構へ依存しないことを確認する。
6. failure時はcredentialを変更せず、retry・別sourceへのfallbackをせず停止し、safe diagnostic（ADR-0066／PB-3m／PB-3m.2、`SafeCredentialSubstage()`／`SafeCredentialCategory()`）を記録する。

**Distribution upgrade**（container自体の検証）: N・N+1ともDeveloper ID署名・notarize・staple・canonical DMG化されていることを確認し、DMG全体のupgrade取得（新しいDMGを別途download）とmountを確認し、Gatekeeper verification（DMG層・3 CLI層の両方、セクションE参照）をN・N+1両方で実施する。

**Credential separation**: 本Acceptance（D）専用のtest credentialをbuild Nで登録し、build N+1でのread完了を確認した後に失効する。セクションC（New-user）とは別のtest credential・別のsessionを使う。Humanの既存real Anthropic credentialは使用しない。Acceptance evidence／記録データ自体は（credential値そのものを除き）削除せず保持する。

## E. Gatekeeper／quarantined download Acceptance（[ADR-0071](adr/ADR-0071-macos-developer-id-signing-and-notarization.md)「10」、`v1.0.0-beta.1`: COMPLETE／PASS）

DMG層と内部3 CLI層を分離して検証します。**両方が必須です（「または」ではありません）。** `v1.0.0-beta.1`ではrelease前Acceptanceに加え、PB-3cmで公開assetのbyte一致、PB-3cnで公開URLからのSafari download、quarantine保持、Finder mount、DMG Gatekeeper、3 CLIのonline notarizationとTerminal実起動を確認しました。

**DMG層**:

1. quarantine属性が実際に付く経路（ブラウザ等）でcanonical Release assetを取得する。
2. `spctl --assess --type open --context context:primary-signature <dmg>`でDMG自体のsignature／notarization／stapleを確認する。
3. FinderでDMGを通常のdouble-click openでmountする（right-click override不要、`xattr`削除不要）。

**内部3 CLI層**（[PB-3u.8](ROADMAP.md)で、今回の対象macOS環境とcandidateにおいて、bare Mach-O CLIへの`spctl --assess --type exec`が「appではない」としてrejectされることを観測しました——普遍的・構造的なmacOS仕様としての断定ではなく、今回の実測結果です。この結果と、Apple DTSのproduct-type別notarization案内に基づき、bare CLIの署名・notarization妥当性は`--check-notarization`で確認します。詳細は[ADR-0071](adr/ADR-0071-macos-developer-id-signing-and-notarization.md)「PB-3u.8b addendum」を参照）:

4. mount済みDMG内の3 binaryそれぞれに`codesign --verify --strict --check-notarization -R="notarized"`を実行する（Apple notary serviceへ通信するonline確認）。
5. Terminalから3 binaryそれぞれのversion metadataを確認し、実際に**quarantine属性が付いたまま**起動する（`workcairn-daemon`はdaemonとして起動、`workcairn`／`workcairn-core`はversion出力とごく短いsmoke）。
6. 特定のGUI dialogが表示されること自体を成功条件にしない——「実際のTerminal起動が拒否されないこと」を確認する。
7. 手順5のTerminal起動がGatekeeperにrejectされた場合はblockerとして扱う。right-click override、`xattr`削除、Security設定の恒久的緩和のいずれによる回避も認めない。

**両層共通**:

8. macOS全体のSecurity設定の恒久的な緩和を要求しないことを確認する。
9. `xattr`削除や右クリックoverrideを標準手順として案内しないことを確認する。
10. DMG層のGatekeeper reject、および手順5の実際のTerminal起動rejectは、known limitationとして容認せず、署名・notarization実装の不備として扱う。bare CLIに対する`spctl --assess --type exec`のrejectは、この判定に使わない（署名・notarization失敗の根拠にしない）。
