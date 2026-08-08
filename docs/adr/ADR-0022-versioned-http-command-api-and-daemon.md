# ADR-0022: version付き同期Command APIをGo daemonの最初の外部入口とする

## Status

Accepted

## Context

ADR-0020の明示RecoveryとADR-0021の主要command向けCommand Ledgerにより、client retryを識別し、`running`を推測再開せず診断できるようになりました。次の自然な入口はHTTP／daemonですが、非同期queue、background resume、Event Outboxを同時に導入すると、現在のcommit pointとRecovery境界を変えてしまいます。

CLIとは別のbusiness ruleをHTTP handlerへ複製せず、長時間Provider command、client切断、process停止を既存Ledger semanticsへ接続する最小の外部契約が必要です。

## Decision

### Versioned command contract

最初のHTTP契約は`workspace-command.v1`とし、`POST /v1/commands`へ次を必須入力します。

- `version`
- `command_id`
- closedな`operation`
- `approved: true`
- operation固有のtyped `payload`

HTTP v1ではCommand IDを必須にします。CLI／公開compatibilityのoptional Command IDは破壊せず維持します。unknown field、unknown operation、oversized body、未承認commandはprocess compositionより前に拒否します。

### Shared application path

HTTP AdapterはCLI processをsubprocess起動しません。`workspace-run`と同じGo process composition、Kernel、Service、Domain、Vault／Provider Adapterを直接呼びます。HTTP固有のrouting、JSON、status codeは`internal/httpapi`に閉じ、Coreへ`net/http`、Vault path、Provider設定を持ち込みません。

daemonはVault rootとProvider設定をRuntime edgeで注入します。`.env` fileは読みません。API payloadからVault root、API key、Provider Base URLを受け取りません。

### Long-running commands and shutdown

v1は同期HTTP requestとしてcommand完了を待ちます。serverのWrite timeoutで長時間commandを途中終了させず、Provider timeoutとrequest context cancellationを既存processへ渡します。client切断後も、既にclaim済みcommandのterminal outcome保存は`context.WithoutCancel`に基づく既存Ledger境界で試行します。

daemonはSIGINT／SIGTERMで新規受付を停止し、設定済み猶予内で実行中handlerを待つgraceful shutdownを行います。猶予超過、process crash、`running` recordを自動resumeしません。

### Inspection and recovery

`GET /v1/commands/{command_id}`は明示された`workspace`または`project` scopeからLedger recordをread-onlyで返します。`COMMAND_IN_PROGRESS`、Ledger invalid、outcome commit失敗、partial terminal resultは`recovery_required`として観測可能にします。自動retry、artifact adoption、Event replay、projection再構築は行いません。

### Exposure boundary

daemonの既定listen addressはloopbackです。認証、TLS termination、multi-tenant authorization、remote exposureは本foundationの範囲外であり、それらを決めるまでは外部networkへ公開しません。

## Consequences

- HTTP clientは必須Command IDにより、同一requestを安全に再送できます。
- CLIとAPIは同じGo business pathを使い、Python interpreterやshell gatewayを必要としません。
- long-running commandは同期接続を占有しますが、durable queueや自動resumeを先取りしません。
- daemon停止時は実行中commandを待ち、完了不明の`running`は既存Recoveryへ委ねます。
- public／remote APIに進む前に、認証、TLS、authorization、request policyを別途確定する必要があります。
