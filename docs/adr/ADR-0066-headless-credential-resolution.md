# ADR-0066: Headless Credential Resolution for Unattended Operation

## Status

Accepted

## Context

WorkCairnの対話的なmacOS First-runは、native hidden inputとSecurity.framework Keychain Adapter（ADR-0044）によりClaude credentialを安全に保存・再読込できます。一方、launchd、CIに近いlocal automation、画面を持たないMac／Linux hostではKeychain accessがauthorization UIやsession状態へ依存し得るため、無人daemonが確実に起動できるcredential sourceではありません。

従来のdaemonは`ANTHROPIC_API_KEY`があればそれを使い、なければKeychainを読む暗黙の順序でした。direct `workcairn` CLIは環境変数だけを読みます。この差は既存利用には有用ですが、headless operatorが「どのsourceだけを使うか」を固定できず、失敗時に別sourceへ進むか否かも明示されていませんでした。

候補を次のように比較しました。

- macOS Keychain: 対話利用には最も自然で、既存First-runとread-backを再利用できる。ただし無人sessionでのauthorization／unlock UIを排除できない。
- process environment: container／service managerと統合しやすく、既存CLI互換がある。ただしprocess環境の可視性とoperator側の注入管理が必要。
- headless local file: OS標準filesystemだけで決定的に読めるが、path、permission、owner、symlinkを厳格に固定しなければ平文secretを広げる。
- external secret manager: rotation／central policyには強いが、Public Betaへ新しいruntime、network、Providerを追加する範囲を超える。
- sourceの明示選択: unattended operationをfail-closedにできるが、既存interactive flowとの互換modeを別に残す必要がある。

## Decision

`workcairn-daemon`のRuntime edgeに、閉じたcredential source contractを追加します。

- `automatic`: 後方互換mode。`ANTHROPIC_API_KEY`、次にmacOS Keychainの順で読む。headless-localは候補に含めない。
- `environment`: process environmentだけを1回読む。Keychain／fileへfallbackしない。
- `keychain`: ADR-0044のKeychainだけを1回読む。environment／fileへfallbackしない。
- `headless-local`: OS user config root配下の固定fileだけを1回読む。environment／Keychainへfallbackしない。

sourceは`--claude-credential-source`で選択します。明示sourceのmissing／unavailableはdaemon startupをfail-closedにし、自動retryや別sourceへのfallbackをしません。`automatic`だけは既存First-runのため、credential未設定でもProvider unconfiguredのredacted statusでdaemonを起動できる従来挙動を維持します。

headless-local pathは任意入力にせず、`os.UserConfigDir()/WorkCairn/credentials/anthropic-api-key`へ固定します。macOSでは通常`~/Library/Application Support/WorkCairn/credentials/anthropic-api-key`です。WorkCairnはこのfileを作成、更新、移行しません。operator／provisionerが明示的に用意したものをread-onlyで扱います。

headless-local loaderは次をすべて満たす場合だけcredentialを返します。

- final pathがregular fileで、symlinkではない。
- modeが正確に`0600`である。
- file ownerがdaemonのeffective userと一致する。
- `Lstat → open → fstat`で同一fileであることを確認する。
- sizeを64 KiBへboundedし、空／whitespace-onlyを拒否する。

loader errorはsourceとclosed classificationだけをRuntimeへ返し、path、raw OS error、credentialをLedger、HTTP、UI、Audit、logへ渡しません。Provider Adapterは解決後の値だけを受け取り、sourceを知りません。

First-run／SettingsのKeychain保存経路は`automatic`／`keychain`だけで利用できます。`environment`／`headless-local`でGUIから接続操作を試みた場合はread-only sourceとして安全に拒否し、Keychain inputを開きません。direct `workcairn` CLIは今回変更せず、明示operator環境の`ANTHROPIC_API_KEY`だけを読みます。

## Consequences

- interactive macOS利用は従来どおりGUIとKeychainで成立する。
- headless daemonはKeychain UIや暗黙fallbackへ依存せず、sourceを明示して起動できる。
- credentialはrepository、Vault、`.env`、browser storage、Command contractへ入らない。
- release binary／archiveへ追加runtime依存はなく、Go Onlyを維持する。
- headless-localは平文fileであるため、host filesystem／backup／ACLの保護はoperator責任として残る。WorkCairnは権限不備を安全拒否するが、secret managerやrotationは提供しない。
- external secret manager、automatic migration、credential rotation、durable credential auditは将来の独立Phaseとし、今回先取りしない。

## Rejected alternatives

- Keychain access失敗時に自動でfileへfallbackする: sourceの意図が曖昧になり、無人運用で予期しないsecretを選ぶため拒否。
- credential file pathを任意CLI flagで受ける: Vault／repository／共有directoryを誤指定できるsurfaceを増やすため拒否。
- WorkCairnがcredential fileを書き込む: secret migrationとlifecycle ownershipが増え、GUI Keychain pathと二重のwriterになるため拒否。
- `.env`またはVaultへ保存する: 既存のGo Only security boundaryとcanonical workspace dataの責務に反するため拒否。
- external secret managerをPublic Betaへ追加する: Provider／network／deployment依存を増やし、本Checkpointのlocal unattended operationを超えるため延期。

## PB-3m addendum（2026-08-30）: 有効startup tupleだけを保持するsanitized診断

**背景**: PB-3j／PB-3kで、packaged daemonが`--claude-credential-source keychain`起動時にKeychain読み取りへ失敗し、`credential_source_unavailable`という単一のgeneric classificationだけが観測されました。source調査の結果、Local OS Adapter（`localos.CredentialError`、ADR-0044）は`CredentialSubstage()`／`CredentialClassification()`というsecret-freeなmethodで、`keychain_not_found`／`keychain_permission_denied`／`keychain_unavailable`等のより詳細な分類をすでに正しく計算していましたが、`runtime.readCredential`がreaderから返る全errorを無条件に`credential_source_unavailable`へ置き換えており、この詳細が一段上のRuntime境界で失われていました（本行が指す「loader errorはsourceとclosed classificationだけをRuntimeへ返す」という既存Decisionの、classification自体が握りつぶされていたという実装gapです）。

**Decision**: `CredentialResolutionError`（`go/internal/runtime/claude_credential.go`）はexported raw diagnostic fieldを一切持ちません。代わりに、Runtime private内部だけで完結するclosedなenum（`credentialDiagnosticOutcome`）へ、Adapterの`(substage, category)`を検証済みの1つの確定値として変換して保持します。既存の`Source`／`Classification`（`credential_source_invalid`／`credential_source_missing`／`credential_source_unavailable`／`credential_source_read_only`）は無変更です。

- RuntimeはLocal OS Adapter packageを一切importしません。`CredentialSubstage() string`／`CredentialClassification() string`という同じmethod shapeを持つprivateな`credentialDiagnostic` interfaceをRuntime側に定義し、`errors.As`による構造的な型一致だけでAdapter errorを認識します（wrapされたerrorも認識します）。Adapter Patternは維持されます。
- `classifyStartupCredentialDiagnostic(source, substage, category)`が、呼び出し元のsourceと合わせて3要素を1つのtupleとして検証します。有効なstartup tupleは次だけです。`source=keychain`＋`substage=keychain_read`＋`category`が`keychain_not_found`／`keychain_permission_denied`／`keychain_command_failed`／`keychain_output_invalid`／`keychain_setup_timeout`／`keychain_unavailable`のいずれか。`source=headless-local`＋`substage=headless_local_read`＋`category`が`credential_file_not_found`／`credential_file_permission_denied`／`credential_file_unsafe`／`credential_file_output_invalid`のいずれか。それ以外（未知のsubstage／category、source と substage の不一致、既知だが他方のvocabularyに属するcategoryとの組み合わせ、`keychain_write`／`keychain_read_after_write`／`credential_input`というFirst-run／Settings専用のsetup substage）はすべて、diagnosticなしのgeneric `credential_source_unavailable`へ安全に退化します。`headless-local`が`keychain_unavailable`を返す組み合わせ（Adapter側のclassification名がheadless sourceと不整合な既知の状態）も、本addendumでは意図的にrenameや特別扱いをせず同様にgenericへ退化させます。
- `CredentialResolutionError`は、`SafeCredentialSubstage()`／`SafeCredentialCategory()`という2つのread-only accessorだけを公開します。両者は上記のclosed enumから固定literalを描画するだけで、Adapterから受け取った文字列そのものを一切保持・返却しません。認識されなかった場合はどちらも空文字列を返します。他packageがこのstructへ任意のdiagnostic文字列を設定できる経路はありません（exportedなsetterやexported raw fieldが存在しないため）。
- `Error()`はこのclosed enumから描画した固定literalだけを表示します。診断がない場合の表示は既存形式と完全一致します（`Claude credential resolution failed for keychain: credential_source_unavailable`）。安全な診断がある場合は括弧付きで追加します（`Claude credential resolution failed for keychain: credential_source_unavailable (keychain_read: keychain_permission_denied)`）。
- raw underlying error、OSStatus数値、command output、path、credential値・長さ・fingerprintのいずれも保持・露出しません。`CredentialResolutionError`は`Unwrap()`を持たず、Adapterの元errorへ到達する経路はありません。
- credential source selection、`automatic`のenvironment→Keychain順序（Keychainへfall backする際のdiagnosticは正しく`source=keychain`として分類されます）、明示sourceのfail-closed、headless-localへの暗黙fallback禁止、retry禁止、credential読取回数は本addendumで変更していません。

**Consequences**:
- packaged daemonのstartup errorから、有効なstartup tupleが認識された場合だけ`(<substage>: <category>)`という安全な形式で追加情報が観測できます。認識されない場合は従来どおりの表示のままです。
- signing、notarization、Keychain ACLの根本原因自体は本addendumの対象外です。安全な診断の保持だけを行い、PB-3nで別途Architecture／ADRとして検討します。
- 過去のADR-0066本文（Status／Context／Decision／Consequences／Rejected alternatives）は変更していません。
