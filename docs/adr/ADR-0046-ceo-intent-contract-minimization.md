# ADR-0046: CEO Intent Contractを最小化する

## Status

Accepted

## Context

ADR-0039はCEO Plan生成をLLM Intent（`project_name` / `objective` / `summary` / `steps[]` / `ceo_questions`）とGo Normalizerへ分離しました。しかし実機Acceptanceで次の失敗が発生しました。

```
Error code:   INTERACTION_PLAN_FAILED
Stage:        ceo_plan_intent
Command ID:   CMD-416EE3D1-E0CA-47AB-BCC2-4B41989F4326
Parse reason: missing_required_field
Parse field:  objective
```

`go/internal/ceoplan/intent.go`の`ParseIntent`は、この失敗の発生箇所を`objective == ""`という一箇所の分岐へ確定させます。調査の結果、次が判明しました。

- `intent.Objective`の唯一の下流消費者は`process/ceo_plan.go`の`planDescription()`で、Project Description（`Objective + "\n\n" + Summary`）を作るためだけに使われます。`NormalizeIntent`はこの値へ一切の意味処理を行わず素通しするだけです。
- CEOが実際に入力した自然文は、Interaction Session作成時点（`interaction.start`）で既に`interaction.Record.Request`として確定・永続化されています。`objective`はこれをLLMが言い換えたものに過ぎず、意味的に重複しています。
- `planDescription()`自身は`summary`が空文字列でも許容する設計（`if summary == "" { return objective }`）である一方、`ParseIntent`は`summary`が空だと即座に失敗させるという内部矛盾がありました。
- `project_name`はdisplay用の提案であり、欠落してもGoが決定的なfallback名を生成できます（既存のProject名衝突時auto-disambiguation機構がその後段で吸収します）。
- `steps`（作業の意味的分解）と`ceo_questions`（真の曖昧性の識別）だけが、LLMの意味理解でなければ決定できない責務です。

この3フィールド（`objective`/`summary`/`project_name`）をProvider必須にし続けることは、Goが既に持っている情報の再生成をLLMへ要求し、Provider/model更新のたびに失敗しうる表面積を不必要に広げていました。

## Decision

CEO Intent Contractを次のように縮小します。

### Provider Structured Output Schema（`ceoplan.IntentJSONSchema()`）

`required`配列を`["project_name", "objective", "summary", "steps", "ceo_questions"]`から`["steps", "ceo_questions"]`へ縮小します。3フィールドは`properties`に残り、Providerが値を返す場合は引き続き`type: "string"`として妥当である必要があります（型は緩めません）。`steps`のitem schema（`anyOf`によるreview/non-review variant分岐、`required_role`の扱い）は今回変更しません。

### Parser（`ceoplan.ParseIntent`）

`project_name`・`objective`・`summary`がmissing・空文字列・whitespace-onlyのいずれであっても、それ単独では`ParseIntent`全体を失敗させません。トリム済みの値（空文字列を含む）をそのまま`Intent`構造体へ保持します。`steps`（`minItems >= 1`、有効な`kind`、非空`description`、non-reviewでの`required_role`）と`ceo_questions`（`null`拒否、空要素拒否）は既存のstrict検証を一切変更しません。unknown field拒否、trailing content拒否などの既存構造的strictnessも維持します。

**「optionalである」ことと「型を問わず受理する」ことは区別します。** 3フィールドは`json.RawMessage`として一度decodeし（`steps[].required_role`の既存`decodeIntentRequiredRole`と同一パターンを再利用した`decodeOptionalIntentField`を新設）、次のように分類します。

| 入力 | 結果 |
|---|---|
| キー欠落 | 許可（空文字列として扱う） |
| `""` | 許可 |
| whitespace-only | 許可（trimして空文字列） |
| 有効な文字列 | 許可（その値） |
| 明示的な`null` | **拒否**（`IntentParseJSONDecodeFailed`） |
| object / array / number / bool | **拒否**（`IntentParseJSONDecodeFailed`、既存のJSON型不一致decode failureと同じ経路） |

「optional化」はProviderへ値を必須要求しないという意味であり、型違反まで黙って許容する意味ではありません。この判定は`steps[].required_role`で既に確立済みのnull拒否パターンをそのまま3フィールドへ適用したものであり、新しいdecoder機構の追加ではないため、CP1の範囲を超える大規模refactorにはなりませんでした。

### Normalizer（`ceoplan.NormalizeIntent`）

`NormalizeIntent`は、Intent側で欠落したfieldに対して**Go側が既に確定的に保持している情報だけ**からcanonical値を決定します。これは推測ではありません。

- **`objective`のfallback**: `IntentContext.Request`——正確には`interaction.Record.PlanningRequest()`が返すcanonical planning input（CEOの元requestに、確認済みclarification回答があればそれを付加したもの。単なる`Session.Request`の生値ではありません）——をtrimしてそのまま採用します。これはSession作成後、Plan生成のたびにGoが既に確定させている値であり、LLMの再呼び出し、要約生成、固定文言（"Untitled objective"等）は一切行いません。
- **`project_name`のfallback**: `IntentContext.SessionID`と`IntentContext.RequestDigest`（どちらも既にSession作成時に確定・永続化済み）だけから決定的に導出します。`time.Now()`、乱数、追加のProvider呼び出しは一切使いません。同じ入力からは常に同じfallback名になり、Command Ledgerのreplay保証と両立します。生成したfallback名は既存のProject名衝突検出・auto-disambiguationへそのまま渡され、新しい別のcollision policyは作りません。
- **`summary`**: fallbackを生成しません。空文字列のまま`Plan.Summary`へ通します。`planDescription()`の既存許容セマンティクスと整合します。将来、Plan確認UIが`objective`（＝canonical PlanningRequest由来）だけで十分な情報量を提供できると判断された場合、`summary`はIntent Contractから完全に削除する候補です（今回は削除しません）。

`NormalizeIntent`のシグネチャに`IntentContext`という最小のtyped contextを追加します（`NormalizeIntent(intent Intent, employees []organization.Identity, context IntentContext) (Plan, error)`）。`IntentContext`は`Request` / `SessionID` / `RequestDigest`という3つのdeterministicな文字列だけを持ち、`interaction.Session`全体をDomainへ渡しません。

`NormalizeCandidate`（既存の最終canonical検証、`NormalizeIntent`・`ParseRunnerOutput`・`ValidateApprovedPlan`が共有）は、`summary`のrequiredText検証だけを緩和し、空文字列を許容します。`objective`と`project_name`のrequiredText検証は変更しません——`NormalizeIntent`が呼び出し前に必ず非空の値を用意するため、この不変条件は保たれます。

### required_role / Assignment

今回は一切変更しません。`kindPreferredRoles`によるwrite fallback（fail-closed、コミット`bffaedd`）、`resolveStepAssignment`、`organization.ResolveTaskAssignment`はそのまま維持します。Organization-constrained enumによる短期対応、およびcapability-based assignmentへの長期移行は別ADRで扱います。

### Diagnostics

`ParseFailureReason`、`IntentParseFailureReason`、`NormalizationFailureReason`のenum値は一切削除しません。`missing_required_field`/`objective`のような過去のLedger recordは、`failure.Envelope`の既存decode互換性の下でそのまま読解可能です。新たに失敗しなくなった分岐（objective/summary/project_name欠落）に対応するテストケースは、成功を期待する形へ更新します。

### JSON Contract整合性

この変更はProvider-facing Structured Output Schemaの`required`縮小と、Go内部（`ceoplan`パッケージ、Public外部契約に現れない）のシグネチャ変更だけです。次を確認しました。

- `ceoplan.Plan` / `ceoplan.ProposedTask`のGo型・JSON shapeは無変更です。
- `interaction`のCommand payload契約（`interaction.plan.generate`等のリクエスト形状）は無変更です——`CEOPlanGenerationInput`/`CEOPlanInput`へ追加するフィールド（`SessionID`/`RequestDigest`）はGo内部の呼び出し専用で、外部Command payloadのJSON shapeには現れません。
- Vault永続化Plan schema（`Reviews`/`Tasks`等）は無変更です。
- `fixtures/ceo/intent_generation_v1.json`のような既存fixtureは、そのまま（3フィールドとも供給済みの入力として）通り続けます——fallbackはfield欠落時だけ発火するため、既存fixtureの結果は変化しません。

これはbackward compatibleなrelaxationです。Providerが引き続き3フィールドを供給する限り、挙動もJSON shapeも既存と同一です。新しいcontract versionやmigrationは不要と判断しました。

## Consequences

- `INTERACTION_PLAN_FAILED`/`ceo_plan_intent`/`missing_required_field`のうち、`objective`・`summary`・`project_name`を原因とするクラスは構造的に発生しなくなります。
- LLMの責務は`steps`（意味的分解）と`ceo_questions`（真の曖昧性の識別）へさらに絞られ、Provider/model更新耐性が向上します。
- `Plan.Objective`は常に非空という既存不変条件を維持したまま、その出所がLLMまたはGo（canonical PlanningRequest）のどちらでもよくなりました。
- `Plan.Summary`は空文字列を許容するようになり、`planDescription()`の既存挙動と初めて整合しました。
- `summary`は将来のIntent Contract完全削除候補として記録されました——削除は今回行いません。
- `required_role`のOrganization-constrained enum化（短期）とcapability-based assignmentへの長期移行は、別ADRで扱う将来作業として維持されます。
- Conversation Projectionと承認回数削減（Single Execution Approval Scope等）は本ADRの対象外で、別ADRとして別途進行します。
