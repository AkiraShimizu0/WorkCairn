# ADR-0027: External Actionはimmutable request evidenceを先行commitして公開する

## Status

Accepted

## Context

Go Only RuntimeはDeliverableまでをcanonical evidenceとして保存し、承認済みCommandをLedger、HTTP、Schedulerから実行できます。次にWordPress等へ成果物を公開するには、Provider Runnerとは別に外部世界を変更するAction境界が必要です。

外部APIはVault transactionへ参加できません。公開後にlocal保存が失敗してもremote postを確実に削除できず、削除は新しい外部副作用です。また、既存Deliverableやremote postをretry時に推測採用すると、異なる本文を同一結果へ関連付けたり二重公開したりする危険があります。

## Decision

### Typed source and approval

最初のActionは`wordpress.post.publish`だけです。`action-wordpress-plan`は既存immutable Deliverableをread-onlyで読み、Project／Task identity、relative reference、全file SHA-256、logical target ID、prospective Command ID、時刻を`workspace-action.v1` intentへ変換します。本文とtitleはProvider呼出しにだけ渡し、intent JSONへ複製しません。

`action-wordpress-publish`とHTTP `action.wordpress.publish`は、同じProject、Task、target、Command ID、時刻に加え、planで表示した`source_sha256`への明示承認を必須とします。実行時Deliverable digestが一致しなければ、ProviderとAction evidenceより前にstale sourceとして拒否します。Command Ledger claimより前にProvider、Action evidence、Eventを開始しません。HTTP payloadとSchedule recordにBase URL、username、application passwordを含めず、credentialはRuntime edgeからだけ注入します。

### Commit ordering

```text
explicit approval
→ project-scoped Command Ledger claim
→ immutable request evidence
→ WordPress Action Adapter
→ immutable result evidence
→ action.completed Event
    → Audit
    → redacted Notification / Metrics
→ Command Ledger terminal outcome
```

request evidenceは`プロジェクト/<name>/.workspace-os/actions/<Command ID SHA-256>.request.json`へatomic createします。source referenceとdigest、Action kind、target、時刻を保持し、Deliverable本文、Prompt、credentialは保存しません。

WordPress Adapterはtyped intentをREST requestへ変換し、`status=publish`を固定します。SDK、Vault、Task状態、Deliverable保存、Audit、retryを知りません。HTTPSを必須とし、Mock testのloopback HTTPだけを許可します。Provider responseはallow-listしたpost ID、link、published statusだけへ変換し、生responseやcredentialをResultへ漏らしません。

result evidenceは同じdirectoryの`.result.json`へimmutable atomic createし、source digest、Provider、external ID、URL、完了時刻を保存します。result evidence commit後だけ`action.completed`を発行します。Action Service／AdapterはTask状態を変更せず、Task lifecycle Eventを発行しません。

### Partial failure and retry

- request commit前の失敗では外部APIを呼ばない。
- request commit後のProvider失敗はintentを保持したpartial failureで、同じCommand IDのterminal replayはProviderを再呼出ししない。
- Provider成功後にresult evidenceがcommitできなければ、remote公開を削除・rollbackせず「外部効果あり、local outcome未確定」のpartial failureを返す。
- result evidence commit後のEvent／Audit／Notification失敗はevidenceとremote postを保持するpartial publication failureとする。
- atomic publication後のdirectory sync等の失敗は`committed=true`をResultに残し、成功扱いしない。
- crashでCommand Ledgerが`running`のままなら自動resumeしない。remote lookup、post adoption、retry、delete、reconciliationは推測せずRecovery境界へ残す。

同じCommand ID／requestの確定terminal replayだけが既存Resultを返します。異requestでのID再利用は拒否します。新しいCommand IDで既存request／result evidenceを採用しません。

## Consequences

- 既存DeliverableをGo Only製品Runtimeから、明示承認と完全なpartial stateを伴ってWordPressへ公開できます。
- Provider credentialとHTTP transportはRuntime／Adapter edgeに留まり、Kernel／Domain／Service／Vault evidenceへ入りません。
- 外部公開後のlocal failureを自動rollbackしないため、事実を失わず手動判断できます。
- HTML変換、update／delete、media upload、remote reconciliation、automatic retry、複数Action providerは未実装です。
