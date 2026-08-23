# ADR-0059: Claude Output Token Policy — a Single, Documented, Runtime-owned Default

## Status

Accepted

## Context

`internal/adapter/claude/runner.go`'s `defaultMaxTokens = 3000` is, in practice, the output-token ceiling for every Claude call in the entire system — no caller (production CLI, production daemon, or the Synthesis Acceptance harness) ever set `ClaudeProcessConfig.MaxTokens` to a non-zero value, so this one private Adapter constant applied everywhere with no documented rationale anywhere in the repository (confirmed by `git log --follow` on the file: the value has been unchanged and unexplained since the Claude Adapter's first commit).

A real Claude one-shot Synthesis Acceptance benchmark (`claude-sonnet-5`, scenario `public-beta-product-growth-ja-v1`) observed `StopReason=max_tokens` at exactly this ceiling: the response stopped mid-way through its first of several planned priorities, before reaching its own validation-method text or any later priority. ADR-0058 (the prior Checkpoint) already ensured this class of event is never silently accepted as a normal completion — the Task is now recorded as a typed `OUTPUT_INCOMPLETE` failure and held, regardless of what the ceiling is set to.

This ADR is a different, narrower question than ADR-0058: **given that truncation is now correctly observable, what should the ceiling itself be, who owns that number, and how is it configured?** Provider call success and Task deliverable completeness are separate questions (ADR-0058); this ADR is about giving that second question — "how much room does a Task get to answer" — a single, explainable, non-arbitrary source of truth, instead of leaving it as an implicit fallback with no documented reasoning.

### 既に決定済み・実装済みで、本ADRが変更しない事項

- **ADR-0058のOUTPUT_INCOMPLETE/typed failure機構**は無変更です。本ADRはceilingの値とその所有者だけを扱い、`max_tokens`到達時のTask取り扱い（Deliverable未保存、TaskService.Fail→Hold）には一切触れません。
- **`worker.StopReason`とその判定ロジック**（前々Checkpoint）は無変更です。
- **`ClaudeProcessConfig.MaxTokens`／`claude.Config.MaxTokens`という既存field**は無変更のまま再利用します。新しいConfig型は追加していません。

### 新たに決定が必要な事項（本ADRのDecision）

1. Claude output token defaultの所有者（package／layer）。
2. production default値と、その値の根拠。
3. Runtime compositionでの明示設定方法。
4. CLI／daemonへのoverride公開要否。
5. production／Acceptanceの設定一致。
6. task-specific policyを今回導入するか。

## Decision

### 1. Owner: `internal/runtime`（既存のADR-0045 timeout policyと同じ構造）

`internal/runtime.DefaultClaudeMaxTokens`（新規、`internal/runtime/claude_output_policy.go`）が、Claude output token policyの唯一のcanonical値です。これはADR-0045が確立した既存patternと同一の構造です——ADR-0045の`internal/runtime.DefaultProviderRequestTimeout`（Provider request timeout policy）が、CLIとdaemonの両方が参照する単一のRuntime composition-owned定数であるのと全く同じように、`DefaultClaudeMaxTokens`もRuntime compositionが所有し、あらゆるcomposition root（`cmd/workcairn`の全10箇所の`ClaudeProcessConfig`構築、`cmd/workcairn-daemon`、`internal/synthesisacceptance`のAcceptance harness）がこの1つの定数を参照します。

`internal/adapter/claude`の既存`defaultMaxTokens = 3000`は変更していません——ただしこれは、値を設定し忘れたcaller向けの**defensive fallback**という位置づけに明確化されます。全productionのcomposition rootが常に明示的に`DefaultClaudeMaxTokens`を渡すようになった今、この fallbackは実運用では発火しません（既存test `TestNewValidatesConfigAndAppliesStableDefaults`が、この fallback自体の安全性を独立して検証し続けます）。二重source-of-truthではありません——「6000という値の意味を決めるのはRuntime、0のときに何か安全な値で埋めるのはAdapterの責務」という異なる責務の分離です。

### 2. Production default値: 6000

実測されたv2 one-shot benchmarkの結果（3000で打ち切られ、内容から判断して全体の40〜50%程度しか生成されていなかった——Section 1／2は完全に生成され、Section 3の最初の優先度の「対応内容」「根拠」までで打ち切られ、「期待される効果」の途中で切れ、後続の優先度には到達しなかった）を、この決定の**repository-derived主要根拠**とします。生成済み部分の構造比率から、完全な応答には概ね3000の1.8〜2.2倍程度が必要と推定し、単純な2倍（3000→6000）という、根拠を追跡しやすい係数を採用しました。恣意的に大きな数字（8192等の慣用値）へ飛びつくのではなく、実測データから直接説明できる値を優先しています。

補助的な確認（**external Provider knowledge、repository外の一般知識であり本Checkpointでは実API呼び出しによる検証はしていません**）: Claudeモデル群は一般に、この程度のmax_tokens値を問題なく受け付ける仕様です。正確な提供元固有の上限は、実際にreal API呼び出しをして確認しない限りrepository単独では検証できないため、本Checkpointでは確認していません。6000という値がモデル自体の上限に抵触するリスクは低いと考えられますが、これは推定であり確定した事実ではありません。

ADR-0045の5分request timeoutとの関係も確認しました。典型的なClaude生成速度（外部知識、未検証）を踏まえると、6000 tokenの生成は5分のtimeoutへ抵触する可能性は低いと考えられますが、これも実測に基づく確認ではなく、注意点として記録するに留めます。

### 3. Runtime compositionでの明示設定方法

各composition rootの`ClaudeProcessConfig{...}`構築へ`MaxTokens: workspaceruntime.DefaultClaudeMaxTokens`を追加しました：

- `cmd/workcairn/main.go`: 全10箇所（構造的に同一だった既存literal）。
- `cmd/workcairn-daemon/main.go`: 1箇所。
- `internal/synthesisacceptance/harness.go`: 1箇所（Acceptance parity、Decision 5参照）。

いずれも既存の`ClaudeProcessConfig.MaxTokens`／`claude.Config.MaxTokens`fieldへの値の注入であり、新しいConfig型、新しいfactory、新しいpolicy抽象化は一切追加していません。

### 4. CLI／daemon override: 追加しない

明確な運用ユースケース（operatorごとの調整が必要な具体的シナリオ）が見当たらないため、今回は`--claude-max-tokens`のようなuser-facing flagを追加しません。ADR-0045のtimeout policyには`--timeout`／`--provider-timeout`という既存overrideがありますが、これは意図的に別判断です——timeoutのoverride需要（ネットワーク環境やdebugging）とは異なり、MaxTokensのoverrideは直接的にoutput token cost（Claudeの課金上、より高価な側）へ影響するため、「configuration surfaceを最小に保ち、偶発的なcost増加を防ぐ」ことを優先しました。

将来、明確な運用ユースケースが生じた場合の拡張は小さく済みます——`ClaudeProcessConfig.MaxTokens`のoverride plumbing自体は既に存在するため、flag追加とその値の受け渡しだけで済み、新しいConfig systemは不要です。

### 5. Production／Acceptance parity

`internal/synthesisacceptance/harness.go`は`workspaceruntime.DefaultClaudeMaxTokens`をそのまま使用し、Acceptance専用の別値は一切設定しません。これは明示的なtest（`TestHarnessUsesTheSameProductionMaxTokensPolicyNotATestOnlySpecialValue`）で、実際のrequestの`max_tokens`フィールドがproductionと同一の値であることを検証しています。Acceptanceだけ大きい値にしてbenchmarkを通しやすくする、という抜け道は構造的に存在しません。

### 6. Task-specific policy: 今回は導入しない（Option A）

Synthesis相当のfan-in task（依存2件以上）は、通常のTaskより長い応答を必要とする可能性がありますが、今回の証拠は「1回の実測truncation」だけであり、通常Taskが3000〜6000の範囲で問題を起こしているという証拠は一切ありません。ceilingを引き上げること自体は、短く終わるTaskのcostやlatencyへほぼ影響しません（`max_tokens`は上限であり、モデルは`end_turn`で自然に停止すれば短い応答のままです）。この非対称性——「上げてもほとんどのTaskは無傷、下げないと一部のTaskが壊れる」——を踏まえ、今回はシンプルな単一共有default（Option A）を採用し、task-type別のspeculative routingは見送りました。

## Rationale（評価軸ごとの整理）

- **Synthesis completeness**: 実測truncation地点の約2倍という直接的な根拠に基づき改善を狙う。ただし実Providerでの再測定なしに完全性を保証するものではない。
- **通常Taskへの過剰なoutput allowance**: ceiling引き上げは通常Taskのcost/latencyへほぼ影響しない（上限は使用量を強制しない）。
- **Claude output cost**: 実際に多くのtokenを生成した場合にのみcostが増える。暴走・反復生成という稀なtail riskのcost上限は6000相当まで広がる——この点は正直にConsequencesで認める。
- **Latency**: 短い応答は無変更。長い応答（Synthesis等）はより長く生成を許可される分、latencyも伸びうる——これはcompletenessとのトレードオフとして意図的に受け入れる。
- **request timeoutとの関係**: ADR-0045の5分ceilingとの抵触リスクは低いと推定するが未検証。
- **model capability**: 外部知識としては十分収まる範囲と考えられるが、repository単独では検証していない。
- **product UX**: truncationによるHeld Taskの頻度低下が期待されるが、実測での確認は次のreal Acceptance実行を待つ。
- **simplicity**: 単一共有default、task-specific routingなし、speculative config systemなし。
- **future configurability**: 既存plumbingにより、将来のoverride追加は小さな変更で済む。

## Non-goals

- Prompt compression、Prompt v3。
- Evaluator（`internal/synthesisacceptance/evaluator.go`）の変更。
- Automatic retry、Provider fallback。
- Dynamic model routing。
- Aggregate Workflow token budget、monetary token budgeting（BudgetGuardの既存スコープ外のまま）。
- Real Claude Acceptance benchmarkの実行（本Checkpointではexternal Provider呼び出しを一切行っていません）。

## Consequences

- **production call behavior**: あらゆるproduction Claude callが、`internal/adapter/claude`の暗黙defaultではなく、`internal/runtime.DefaultClaudeMaxTokens`（6000）を明示的に使用するようになりました。
- **cost/latency**: 短い応答のcost/latencyは無変更。長い応答（Synthesis等）はより長く生成を許可されるため、真に暴走した場合のtail cost/latencyの上限も相応に広がります——これは意図的に受け入れたトレードオフです。
- **truncation probability**: 実測地点の約2倍のheadroomにより、同種のSynthesis truncationの再発確率は下がると期待されますが、実Providerでの再測定なしに保証はできません。
- **future configuration**: CLI／daemon overrideは追加していませんが、既存plumbingにより将来の追加コストは小さいままです。task-specific routingも、明確な証拠が集まった場合に別ADRとして追加可能な設計のままです。
- **release/benchmark implications**: 次のreal Claude Acceptance実行（`make synthesis-acceptance PROVIDER=claude EXECUTE=1`）は、production defaultと完全に同一の設定で実行可能な状態です。Prompt、Evaluator、Scenario、Modelはいずれも本Checkpointで変更していません。

## Rejected Alternatives

- **`defaultMaxTokens`をClaude Adapter package内で直接3000→6000へ書き換えるだけ**: Adapterの private constantを「production policy」として扱い続けることになり、根拠のない暗黙default構造自体は解消されないため却下。
- **Acceptance-only override（Acceptanceだけ大きい値を使う）**: production/Acceptanceの設定が乖離し、benchmarkが実際のproduction挙動を測らなくなるため明示的に却下（Step 8指示）。
- **Task-type別のspeculative routing**: 証拠が「1回の実測truncation」のみで、複雑さに見合う根拠が不足しているため却下。将来証拠が蓄積すれば再検討可能。
- **新しいTokenPolicyManager／ProviderBudgetManager等の抽象化**: 既存の`ClaudeProcessConfig.MaxTokens`／`claude.Config.MaxTokens`で十分表現できるため、speculative abstractionとして却下。
- **CLI override flagの今回追加**: 明確な運用ユースケースが無いため見送り。将来必要になれば既存plumbing上に小さく追加できる。
- **8192等の慣用値を直接採用**: 実測データに基づかない選択となり、「なぜその数字か」を説明できなくなるため却下。2倍という実測ベースの係数を優先した。
