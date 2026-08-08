# ADR-0017: Employee rename batchは全件preflight後に単一rename commitを順次調停する

## Status

Accepted

## Context

ADR-0015は1社員のrename commit pointとpartial failureを定義しました。Python `EmployeeRenameService`はbatch全体をbackupしてrollbackしますが、process crashを越えるatomic transactionではありません。一方、batch専用の新しい永続形式やtransaction protocolはv0.4 durabilityを先取りします。

## Decision

Go v0.3のbatch renameは、全対象ID、旧氏名、候補間氏名policy、rename先、各単一rename planをread-onlyで先に検証します。1件でも不正なら書きません。明示承認後はADR-0015の単一renameをrequest順に実行し、各社員ごとにimmutable intentとIdentity commit pointを持たせます。Python gatewayはGo batch planとGo単一executeだけを調停し、Python `EmployeeRenameService`へfallbackしません。

batchの途中で失敗した場合、完了済み単一renameをrollback・削除せず、完了結果と失敗した単一renameのpartial stateを返します。候補名はbatch内で予約します。現行単一renameでも安全に実行できることをpreflight条件とするため、Python batchより拒否が厳しい場合があります。

batch全体のatomicity、単一batch intent、retry、adoption、reconciliation、crash recoveryはv0.4へ延期します。

## Consequences

- 全件の静的問題を副作用前に検出しつつ、確立済みADR-0015境界を再利用できます。
- batch failureはrollbackされたように見せず、実際に成立したIdentityを観測できます。
- Python EmployeeRenameServiceは公開API互換／referenceだけに残り、通常writer経路から外れます。
