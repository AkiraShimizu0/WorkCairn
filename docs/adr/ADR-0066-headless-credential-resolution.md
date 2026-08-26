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
