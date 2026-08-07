# ADR-0006: WorkerとRunnerをProvider非依存の境界で分離する

## Status

Accepted

## Context

Python実装ではWorker、PromptBuilder、ModelRouter、ClaudeRunnerがAI実行を構成しています。これらをGoへ移す際、Task lifecycle、プロンプト生成、モデル選択、Provider SDK呼び出しを同じサービスへ集約すると、Kernelが特定LLMや設定方法へ密結合します。また、timeoutやcancellation、Provider固有エラー、不正レスポンスを一貫した契約へ変換する境界が必要です。

## Decision

- WorkerServiceは社員、タスク、プロジェクトの構造化Contextを受け、PromptBuilderとRunner Resolverを順に利用して1件の仕事を実行します。
- PromptBuilderは独立したportとし、System/User Promptを返します。プロンプト内容のGo移植は別段階で行います。
- RunnerはProvider Adapterとし、model、system prompt、user prompt、metadataだけを受け取ります。Task、Project、Markdown、承認、Retry Policyを知りません。
- Runner Registryは`model value → runner name → Runner`を明示的に解決し、未知modelと未登録Runnerを区別します。暗黙fallbackは行いません。
- `context.Context`をWorkerServiceからPromptBuilderとRunnerへ伝播し、timeoutとcancellationを安定したエラー分類へ変換します。
- KernelはWorkerService interfaceだけを保持し、Claude、OpenAI、Gemini、Ollama、APIキー、`.env`を知りません。Provider設定は将来のRuntime／Config／Adapter層がcomposition時に注入します。
- WorkerServiceはTask状態、Deliverable、Audit、承認、retryを扱いません。成功結果はTask完了を意味せず、呼び出し側がTaskServiceへ明示的に反映します。
- 初期版では`WorkerStarted`等のEventを追加しません。Task lifecycle Eventとの意味の重複を避け、Worker固有の監視要件が確定してから追加を判断します。
- Python Worker、PromptBuilder、ModelRouter、Runnerは移行期間中だけ維持し、対応するGo実装とRuntime Adapterの完成後に廃止します。

## Consequences

- Fake PromptBuilderとFake RunnerだけでWorker実行契約、ルーティング、usage、duration、error、cancellationを検証できます。
- Provider SDKを追加してもKernel、TaskService、Worker Domainは変更不要です。
- Runtimeが依存を構成しないDefault KernelのWorkerServiceは起動できますが、実行は安全に拒否されます。
- TaskServiceとWorkerServiceの本番調停、承認、Retry／Hold Policy、成果物保存は今後のWorkflow／Runtime／Adapter実装が必要です。
- Providerのtoken usageが取得できない場合を許容するため、usage値はoptionalです。
