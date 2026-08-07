# ADR-0002: PythonとGo CoreをJSON Contractで疎結合にする

## Status

Accepted

## Context

Go Coreへの段階移行中は、Python AdapterからGoのドメインロジックを安全に利用する必要があります。cgoやPython埋め込みを採用すると、ビルド、配布、障害分離が複雑になり、どちらかのランタイム実装へ強く依存します。

## Decision

PythonとGo Coreの境界には、version付きJSON Contractを使用します。Python Adapterは`workspace-core`をサブプロセスとして起動し、標準入力へ1件のJSONリクエストを渡し、標準出力から1件のJSONレスポンスを受け取ります。

契約は`version`、`operation`、`payload`、`result`、機械判定可能な`error`で構成します。標準出力にはJSON以外を出力せず、診断ログが必要な場合は標準エラーを使用します。契約変更は後方互換な追加を優先し、破壊的変更には新しいcontract versionを使用します。

## Consequences

- PythonとGoは互いの内部型やランタイムへ依存しません。
- 同じCLI境界を将来の別Adapterや外部プロセスから利用できます。
- subprocessの起動コスト、timeout、入出力サイズ、異常終了をAdapterで管理する必要があります。
- 共有fixtureと契約テストをPython・Goの双方で維持する必要があります。
- Python廃止後も、JSON Contractは外部Adapter向けの安定した境界として維持できます。
