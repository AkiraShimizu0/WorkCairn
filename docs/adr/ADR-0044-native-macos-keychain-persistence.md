# ADR-0044: macOS Keychain永続化をnative helperへ移す

## Status

Accepted

## Context

ADR-0038のMac native inputからClaude credentialをKeychainへ保存する境界は成立していましたが、`security add-generic-password -w`へpseudo-terminalでcredentialを渡す実装は、daemonからの実機操作で停止しました。prompt出力との同期を追加しても、`security`／`readpassphrase`／PTYのterminal状態に依存し、15秒のbounded timeoutまでchildが終了しない事例が再現しました。timeout延長は不定な対話processを長く待つだけで、保存commit pointを決定的にしません。

`security`の非対話形式でcredentialをcommand argumentへ渡す方式は、process listや診断へsecretを露出し得るため採用できません。stdin pipeも`security`の対話password入力契約では安定せず、shell、平文file、browser storage、`.env` fallbackもADR-0036/0038のsecret境界に反します。GoからSecurity.frameworkを直接呼べば対話prompt parsingを除去できますが、native callがOS内部で停止した場合に同じdaemon process内から安全に中断できません。

## Decision

macOS Local OS Adapterは、同じ`workcairn-daemon` executableを短命なisolated helper processとして起動し、helperだけがcgoでCoreFoundation／Security.frameworkの`SecItemAdd`、`SecItemUpdate`、`SecItemCopyMatching`を呼びます。別binary、shell、Node、Python、外部runtimeは追加しません。Core／Domain／ServiceはmacOS APIを知りません。

credentialはMac native hidden-inputから親process memoryへ入り、継承したanonymous Unix-domain socket file descriptorだけを通ってhelperへ渡ります。argv、environment、stdin、stdout、stderr、HTTP、browser storage、Vault、Command Ledger、Audit、logには載せません。helper protocolはlength-framed binaryとsafeなOSStatusだけを返し、raw Keychain outputやcredentialをerrorへ含めません。serviceは`com.workcairn.provider.anthropic`、accountは`api-key`で固定し、別identifierへfallbackしません。

saveは`SecItemAdd`を行い、`errSecDuplicateItem`だけを`SecItemUpdate`へ分岐します。成功後は同じservice/accountを新しいbounded helperでread-backし、non-emptyを確認して初めて成功とします。startupも同じread operationを使います。Keychain UIを暗黙表示せず、interactionが必要、permission denied、locked/unavailable、not foundをsafeなtyped classificationへ変換してdefault denyします。

各native operationは15秒に制限します。Security.framework call自体をin-process cancellationしようとせず、context expiry時にhelper processをkillし、`Wait`でreapしてからtimeoutを返します。自動retry、別保存先、別service/account、成功推測は行いません。Mac input dialogの2分、browser requestの3分という既存外側timeoutは変更しません。

macOS release binaryはSecurity.frameworkをlinkするためcgoを必要とします。macOS archiveはmacOS build hostで`CGO_ENABLED=1`としてbuildし、arm64／amd64を検査します。非macOS CIのmatrixはdarwinのportable no-cgo stubまでをcompileできますが、darwin archive作成とnative Keychain検証は拒否します。Linuxは既存どおり`CGO_ENABLED=0`で、macOS Keychain Adapterは利用しません。製品Runtimeは引き続きGo Onlyで、追加language runtimeやpackage dependencyはありません。

## Consequences

- PTY prompt検出、terminal echo、`readpassphrase`、short write raceを製品credential pathから除去します。
- initial save、existing item update、read-back、daemon restart readが同一native contractとservice/accountを使います。
- timeout時にdaemonを止めずhelperだけを確実にkill/reapでき、UIは既存typed failureでterminal stateへ戻ります。
- unit testはnative boundaryをfakeし、helper protocol、short write、OSStatus分類、timeout cleanup、secret非露出を実Keychain変更なしで検証します。実Keychainのpermission／unlock状態はPublic BetaのMac human acceptanceで確認します。
- macOS cross-releaseを非macOS hostから作成する経路は拒否します。darwin native build matrixはmacOS hostが必要です。

## Alternatives considered

- **`security` + PTY**: secretをargvへ出さない一方、daemon環境でのTTY／prompt／readpassphrase状態が不定であり不採用。
- **`security -w <credential>`**: 非対話で決定的でもsecretがargv／process inspectionへ出るため不採用。
- **`security` stdin pipe**: password readが通常stdin pipeを安定したcontractとして扱わず不採用。
- **Security.frameworkをdaemon processから直接呼ぶ**: CLI依存は消えるがnative call停止時にdaemonごと巻き込むため不採用。
- **third-party Keychain CLI/library、pure-Go FFI**: shell実装、追加runtime、beta FFI dependency、Security.framework wrappingの複雑性を増やすため不採用。
