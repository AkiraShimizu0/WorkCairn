# ADR-0036: Provider接続状態とRuntime routingを分離する

## Status

Accepted

## Context

Public Beta Acceptanceで、旧Python版がrepository rootの`.env`を自動読込していた一方、Go Only版は安全境界としてdaemon起動processのenvironmentだけを読むため、過去credentialが存在しても認識されない状態が確認されました。Plan生成失敗はredactedな`INTERACTION_PLAN_FAILED`だけで表示され、利用者は設定不足とProvider障害を区別できませんでした。

またInteraction UIは依頼ごとに論理model名を入力させていましたが、WorkCairnの製品体験は「AIサービスを接続し、社員の役割と仕事に適した頭脳をWorkCairnが選ぶ」です。Provider credential、Provider model ID、Employee Markdownの論理model、Task routing policyを一つの値として扱うと、Provider-neutral境界とAutonomy Contractのallow-listが曖昧になります。

## Decision

### Connection boundary

Provider credential、Provider model ID、Base URL、HTTP clientは引き続きprocess／Runtime edgeだけからAdapterへ注入します。WorkCairnは`.env`、Vault、browser storage、Command payloadからcredentialを自動探索・移動・保存しません。Python版の暗黙`.env`読込を復活させません。

Local Web UIの`AI Connections`はredactedな接続状態とAutomatic routing方針を表示するread-only foundationとします。trusted LAN mobile modeは暗号化されていないため、iPhoneからcredentialを入力・送信・保存するendpointを提供しません。Public Betaでbackend-sideの永続接続を追加する場合は、Macのloopback限定Settingsからsame-originで受け、Runtime edgeのCredential Store Adapterを通してmacOS Keychainへ保存する方式を第一候補とします。secretをresponse、log、Audit、Vault、browser storageへ返さず、process memoryへ必要時だけ取り出します。Linux等は各OS credential facilityを別Adapterとして追加し、平文設定fileを共通fallbackにはしません。実credentialの移行や保存開始はOperatorの明示操作を必要とします。

daemonは起動時に現在注入されたClaude Adapter設定をnetwork accessなしで検証し、redactedなread-only statusを同一origin APIへ公開します。statusはProvider種別、設定可否、`credential`／`provider_model`等の欠落category、設定不正categoryだけを返し、値、長さ、fingerprint、model ID、Base URLを返しません。`configured`はAdapterを構築可能という意味で、remote credentialの有効性やProvider到達性を保証しません。

Interaction Plan生成前に設定不足を検出した場合、`PROVIDER_CONFIGURATION_REQUIRED`／`provider_configuration`としてCommand Ledgerへterminal failureを記録し、Providerを呼びません。UIはPlan承認操作の代わりにMac側の接続設定とdaemon再起動を案内します。別Providerへのfallback、自動retry、terminal failureの再実行は行いません。

### Automatic selection foundation

通常の新規Interactionは、依頼ごとのmodel入力を要求せず、version 1に後方互換な論理値`workcairn-auto`をSessionへ固定します。既存Session、CLI、HTTP callerが明示modelを送る経路は維持します。これはProvider model IDではなく、Runtime routing要求です。

将来のroutingはProvider／Vault非依存のServiceとして、次のtyped inventoryとpolicyから単一の明示Routeを解決します。

- Employee ID／RoleとEmployee Authority
- Task kind／capability requirement
- Runtime edgeが宣言する接続済みProvider／Runtime capability
- quality／cost／latency preference
- Workflow Autonomy ContractのEmployee／model allow-list
- MakerとReviewerのseparation preference

Routeは論理model、Runner identity、Provider connection identity、capability、選択理由を持ち、Provider credentialや生model catalogを持ちません。Runner Registryは解決済みRouteのRunnerを取得する既存境界として再利用します。候補がない、allow-listと交差しない、Reviewer separationを満たせない場合はdefault denyで停止します。未接続Providerへのfallbackや、承認済みrouteを実行中に差し替えることは禁止します。

Public Beta Acceptance修正では新しいProvider、Subscription-backed Runtime、role catalog、cost model、Advanced Settings永続化は実装しません。現在は接続済みClaude Adapterをconnection defaultとして使い、論理`workcairn-auto`を既存Provider-neutral Prompt／Runner要求へ渡します。

## Consequences

- 過去credentialがファイルに存在しても、現在processへ注入されていなければ「接続済み」と誤表示しません。
- credential未設定はProvider transport failureと区別され、Provider呼出し前に安全に案内されます。
- iPhoneとMacのSettingsで接続状態とAutomatic routingを確認できますが、credential登録はまだ行わず、trusted LANへsecret mutation surfaceを増やしません。
- 利用者は通常の依頼でModel名を選ばず、WorkCairnが選ぶ製品体験へ移行できます。
- Core、TaskService、Approval、Command Ledger、Recovery、JSON Contract v1は変更しません。
- 本格的なRole／Task routingは複数Provider／Runtime inventoryを導入するフェーズで、typed Routeとapproval digest／Ledger identityへの拘束を別途実装します。
