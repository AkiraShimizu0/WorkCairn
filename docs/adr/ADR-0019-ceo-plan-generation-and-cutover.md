# ADR-0019: CEO plan生成と適用をGo typed contractで分離する

## Status

Accepted

## Context

Python `CEOCommandService.plan`はOrganizationを読み、ProviderへPromptを送り、任意JSONをPythonで正規化していました。apply writerはADR-0018でGo化できましたが、通常callerがPython Prompt、Python Provider SDK、Python validationを通る限りPython runtimeを製品経路から外せません。またLLMへ正式なProject IDやTask IDを決めさせるとIdentityの正本がProvider出力になります。

## Decision

CEO plan生成の製品入口を`workspace-run ceo-plan-generate`へ移します。処理順は次です。

```text
explicit Provider-call approval
→ Vault Organization Adapter
→ structured Employee inventory
→ provider-neutral CEO Plan Service / canonical Prompt
→ existing Runner interface / Claude Adapter
→ strict JSON parser
→ Go CEO Plan Domain validation
→ plan_only typed result
```

Provider固有設定、API key、HTTP transportはprocess compositionで注入し、CEO Plan Domain／Serviceは`.env`、Vault path、Markdown、Anthropicを知りません。RunnerはTask状態、Project作成、承認、Auditを知りません。

LLM出力には一時的な`PROPOSED-NNN`だけを認めます。正式なProject IDは人間が適用時に指定し、Task IDはADR-0013のTask Domain／TaskStoreから発行します。未知社員ID、重複社員ID、空フィールド、不正Project名、不正Task title、未知・自己・重複・循環dependencyはWorkspaceへ書く前にGoで拒否します。

適用は`workspace-run ceo-plan-apply-plan`と明示承認付き`ceo-plan-apply`へ分離します。applyは受け取ったtyped planを現在のOrganizationに対して再検証し、ADR-0018のProject→TaskService.Create→Task Dependencies orderingを使用します。LLM出力を直接Markdownへ書きません。途中失敗は成立済みProject、Task、Event、Auditを保持したpartial failureです。

生成したplanの永続Store、署名、adoption、retry、reconciliation、Command Ledgerは導入しません。callerはversion付きJSON responseを確認してapplyへ明示的に渡します。これらのdurabilityはv0.4へ延期します。

Python `WorkspaceRunCEOPlanGateway`／`WorkspaceRunCEOApplyGateway`は公開Python caller向けprocess Adapterで、Go失敗時のPython planner／writer fallbackを持ちません。Python `CEOCommandService`はこれらが注入された通常構成ではlegacy validationとapply orchestrationを迂回し、公開互換referenceとしてだけ残します。

## Consequences

- 自然言語依頼からProject／Task作成まで、Goのtyped validationと既存writerだけを通る製品経路を持てます。
- Provider呼出し承認とWorkspace変更承認を別々に扱えます。
- Project／Task IdentityをLLM出力から分離できます。
- Python CEO Prompt、Provider Runner、validationは通常製品経路から外れ、共有fixtureとして移行互換性を検証できます。
