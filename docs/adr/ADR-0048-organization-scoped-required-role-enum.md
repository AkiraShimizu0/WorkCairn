# ADR-0048: Organization-scoped Required Role Enum — Short-term Bridge toward Capability-based Assignment

## Status

Accepted

## Context

CEO Intent（ADR-0039、ADR-0046）の`steps[].required_role`は、Providerが自由文字列として生成する値です。実機Acceptanceで次のクラスの失敗が発生しました。

```
Error code:   ASSIGNMENT_NO_MATCH (assignment_no_match)
Stage:        ceo_plan_normalization
```

原因を調査したところ、次が判明しました。

- `organization.ResolveTaskAssignment`は`RequiredRole`文字列をOrganization roster内の`Identity.Role`と厳密一致（既存の`kindPreferredRoles`によるwrite fallback以外）で照合します。
- Providerは意味的には妥当だが、現在のOrganizationには実在しないRole名（例: `Writer`、`Copywriter`、`Content Creator`）を生成することがあります。canonical Organizationには例えば`Product Manager`・`Content Writer`・`QA Engineer`が実在しますが、Providerがこれらの表記に正確に合わせられなかった場合、`ResolveTaskAssignment`は一致するEmployeeを見つけられず`assignment_no_match`で失敗します。
- 従来のPrompt（`ceoplan.BuildPrompt`）は既存社員一覧を自然文の参考情報として提示するのみで、`required_role`に許される値集合をProvider Structured Output Schema自体では一切制約していませんでした。Schema（`ceoplan.IntentJSONSchema`）側の`required_role`は単なる`{"type": "string"}`であり、Provider側の生成をSchemaレベルで縛る手段が存在しませんでした。

この問題はProvider/model更新のたびに再発しうる表面積であり、Organizationのroster変化（新しいRoleの追加、既存Roleの改名）にも追従できていませんでした。

## Decision

`steps[].required_role`のStructured Output Schemaを、**現在のOrganization rosterが実際に保持するcanonical Role titleのenum**へ制約します。これは明示的な short-term bridge であり、将来のcapability-based assignment（後述）へ置き換わることを前提とした設計です。

### 1. Organization Role source（現状の取得箇所）

`organization.Identity{ID, Name, Department, Role, CurrentTask, Status}`の`Role`フィールドが唯一のsourceです。Prompt/Intent-request構築時点（`service.CEOPlanService.Generate` → `ceoplan.BuildPrompt` / `ceoplan.IntentJSONSchema`）で渡される`[]organization.Identity`（呼び出し元がOrganization台帳から都度読み出し済みのroster snapshot）から、Employee名・IDを一切含まない形でRole titleだけを抽出します。

### 2. Dynamic schema input

`ceoplan.IntentJSONSchema()`（引数なし・静的）を`ceoplan.IntentJSONSchema(allowedRoles []string) (map[string]any, error)`へ変更しました。加えて、抽出専用の新規exported関数`ceoplan.CanonicalRoleTitles(employees []organization.Identity) []string`を追加しました。両者を分離した理由は、抽出（個々のroster不備には寛容）と、Schema生成時のfail-closed判定（0件は不可逆的に拒否）という異なる責務を1つの関数に混在させないためです。

呼び出し側（`service.CEOPlanService.Generate`）は次のように、Prompt構築とSchema構築の両方に**同一の`employees`入力**から`CanonicalRoleTitles`を呼び、その戻り値を`IntentJSONSchema`へ渡します。

```go
schema, err := ceoplan.IntentJSONSchema(ceoplan.CanonicalRoleTitles(input.Employees))
```

### 3. Normalization / dedup / sort rule（決定的順序）

`CanonicalRoleTitles`は次の手順で決定的にRole titleを構築します。

1. 各`Identity.Role`を`strings.TrimSpace`でtrimします。
2. trim後に空文字列となったRoleは除外します（malformed entryとして無視、hard failureにはしません）。
3. 既出のRole titleと重複するものは除外します（同一Role titleの複数Employeeが1エントリへ収束）。
4. 残った集合を`sort.Strings`で辞書順ソートします。

Organization roster（Vault上のEmployee列挙順）がGoのmap iteration順や永続化順に依存して不安定であっても、この関数は入力の順序に一切依存しない出力を返します。`TestIntentJSONSchemaIsByteEquivalentAcrossOrganizationInventoryOrder`（`go/internal/ceoplan/schema_test.go`）が、同じRole集合を異なる順序・重複ありで含む2つのrosterから、`IntentJSONSchema(CanonicalRoleTitles(...))`のJSON直列化バイト列が完全一致することを固定しています。

### 4. required_role最終schema

非review step（`write`/`research`/`analyze`/`implement`）とreview stepの両variantで、`required_role`のproperty schemaを次の形へ変更しました。

```json
{
  "type": "string",
  "description": "The exact Organization role required for this step.",
  "enum": ["Content Writer", "Product Manager", "QA Engineer"]
}
```

`enum`の値は`CanonicalRoleTitles`が返す配列そのものです。`type`/`description`は既存のまま維持し、Anthropic Structured Outputsがサポートするキーワード集合（`type`/`properties`/`required`/`additionalProperties`/`items`/`minItems`/`anyOf`/`enum`/`const`/`description`）の範囲内に収まっています（`TestIntentJSONSchemaUsesAnthropicSupportedSubset`で継続的に固定）。

### 5. Review step semantics（変更なし）

review stepでは`required_role`は従来通り`required`配列に含まれず（`required: ["kind", "description"]`のまま）、省略可能です。ただしProviderが値を返す場合、その値は同じenum制約下にあります——review stepでも「Providerが値を返すなら制約された語彙内でなければならない」という一貫性を維持し、review用と非review用で異なるenum・異なる語彙を持つことはありません。

### 6. No-role / malformed時のfail-closed

`IntentJSONSchema`は`len(allowedRoles) == 0`の場合、`nil`と新設のsentinel error `ceoplan.ErrNoAllowedRoles`を返します。**このケースでは自由文字列schemaへの自動fallbackを行いません。** これはOrganization rosterが空、あるいは全EmployeeのRoleが空文字列のみである（malformed）場合に相当し、この状態でProviderへ制約なしのrequired_role schemaを送ることは、本ADRが解決しようとしているProvider語彙drift問題をそのまま再導入することになるため、意図的に禁止しています。

呼び出し元（`service.CEOPlanService.Generate`）はこのエラーを既存の`CEOPlanPromptStage`（Provider呼び出し前のrequest準備段階の失敗を表す既存stage）へ分類し、Provider呼び出し自体を行いません。新しいCEOPlanStage enum値は追加していません。

個々のEmployee単位のmalformed data（Role空文字列）は、`CanonicalRoleTitles`の抽出段階で静かに除外されます（hard failureにはしません）——0件という集約結果に到達して初めて`IntentJSONSchema`がfail-closedします。これにより「一部のEmployeeデータが多少荒れていても、Organization全体としてまだ有効なRoleが1つでも存在すれば動作を継続する」という耐性と、「本当に使えるRoleが1つもない場合は確実に止まる」というfail-closed原則の両立を図っています。

### 7. Promptとの Role source統一

`ceoplan.BuildPrompt`は、`renderEmployeeContext`（department/id/role付きの既存社員一覧。IDは出力禁止の指示文で明示、意味的context提供が目的で変更せず維持）とは別に、`required_role`の指示文を動的に構築するようになりました。**PromptとSchemaは同一の`CanonicalRoleTitles(employees)`呼び出しを、同一の`employees`引数に対して行う**ため、両者が異なるRole語彙を持つ状態は構造的に発生しません。

```go
if allowedRoles := CanonicalRoleTitles(employees); len(allowedRoles) > 0 {
    requiredRoleInstruction += "次のいずれか1つを、表記のまま正確に使用してください: " + strings.Join(allowedRoles, ", ") + "。"
} else {
    requiredRoleInstruction += "既存社員一覧のRole表記に合わせてください。"
}
```

`allowedRoles`が0件の場合、`BuildPrompt`自体は失敗させず既存の一般的な指示文へ縮退します——このパスがそのままProviderへ送られることはありません。実際にProviderを呼び出す直前、`CEOPlanService.Generate`内の`IntentJSONSchema`呼び出しが同じ0件条件を検知し、fail-closedで停止するためです。Prompt側の縮退とSchema側のfail-closedは、それぞれ異なる責務（Prompt: Providerへの自然文ガイダンス、Schema: 契約上の制約）を持つため、片方だけ緩く・片方は厳格という設計は意図的です。

### 8. Provider-neutralである根拠

`ceoplan.CanonicalRoleTitles`・`ceoplan.IntentJSONSchema`はいずれも`go/internal/ceoplan`パッケージに存在し、`adapter/claude`を一切importしません。Provider固有のRole名マッピングや、Anthropic固有の後処理はこれらの関数に一切含まれません。`adapter/claude`（`runner.go`）は、`ceoplan`が構築済みの`map[string]any`をそのまま`output_config.format.schema`として直列化するだけで、Role enumの中身について一切の判断を行いません。この構造は、CEO Intent Schema全体（`steps`のanyOf構成、`ceo_questions`のitem schema等）が既にProvider-neutralに設計されていた既存方針をそのまま踏襲したものです。

### 9. Assignment resolverを変更していないこと

本ラウンドでは次を一切変更していません。

- `organization.ResolveTaskAssignment`のマッチングロジック
- `kindPreferredRoles`によるwrite → Content Writer fallback（コミット`bffaedd`）
- assignment ambiguity（複数Employeeが同一Roleを持つ場合の曖昧性）semantics
- `assignment_no_match`のエラー分類・semantics
- Reviewer assignment
- capability policyという概念自体（そもそも未実装のまま）

構造として、Providerがenum準拠かつOrganizationに実在する正しいRoleを返した場合、既存のresolverはそれをそのまま解決します。本ADRの変更は「Providerが返しうる値の集合を狭める」ことでこの既存resolverへの入力品質を高めるものであり、resolver自体のロジックには一切踏み込んでいません。

### 10. CP1（ADR-0046）・CP3（ADR-0047）への影響

- CP1が縮小した top-level `required`配列（`["steps", "ceo_questions"]`のみ、`project_name`/`objective`/`summary`はoptionalのまま）は変更していません。`TestIntentJSONSchemaShape`で継続的に固定しています。
- CP3（Conversation Projection）が依存する`ceoplan`パッケージの型・契約には変更がなく、影響はありません。

### 11. Serialized request fixture

`fixtures/provider/claude_ceo_intent_request_v1.json`（`adapter/claude`の`TestRunnerSerializesCEOIntentRequestFixture`が参照する、実際にRunnerが直列化するbytesを固定するfixture）を、`required_role`のproperty schemaに`"enum": ["Content Writer", "Product Manager", "QA Engineer"]`を追加し、`description`を実装の文言（"The exact Organization role required for this step."）へ一致させる形で更新しました。このfixtureはhand-craftedな「理想形」ではなく、テスト内で`ceoplan.IntentJSONSchema(["Content Writer", "Product Manager", "QA Engineer"])`を実際に呼び出しRunnerが直列化した実バイト列と`reflect.DeepEqual`（JSON構造比較）で一致することをテスト自体が保証しています。

## Capability long-term direction（ドキュメントのみ、本ラウンドでは未実装）

`required_role`のOrganization-scoped enum化は、短期的な緩和策であり恒久設計ではありません。長期的には次の方向へ移行する構想です。

- **短期（本ADR）**: `required_role` = 現在のOrganization roleのenum。Provider（LLM）が「どのcanonical Role titleが必要か」を直接選択する責務を持ち続けます。
- **長期（未実装・将来ADR対象）**: `steps[].capability` = Provider-neutralな closed enum（例: `content_creation` / `quality_assurance` / `planning`等、Organization roster非依存の抽象的な作業カテゴリ）。ProviderはこのCapability enumだけを選択し、具体的なRole名の知識を一切持ちません。Capability → Roleの変換はGo側が所有する新設`AssignmentPolicy`（未実装）が担い、`AssignmentPolicy`がOrganization roster上のcanonical Roleを解決し、既存のOrganization resolverへ渡してEmployeeを決定します。
- この長期moveが完了した時点で、`required_role`はdeprecation candidateとなります。Provider-facing schemaからRole名という実装詳細（Organizationごとに異なりうる、Starter Organization固有の語彙を含む）を完全に排除し、Provider-neutral性をさらに高めることが目的です。

本ラウンドでは`capability` fieldの追加、`AssignmentPolicy`の新設、いずれも行っていません。これらは将来の設計・実装の対象として記録するに留めます。

## 明示的に対象外（本ラウンド）

capability field追加、`required_role`削除、新規`AssignmentPolicy`構築、CP4（Approval簡略化）、Conversation Projection変更、HTTP/UI変更、commit/push/PR。

## Consequences

- ProviderがOrganizationに存在しないRole名（`Writer`、`Copywriter`等）を生成することによる`assignment_no_match`は、Structured Output契約レベルで構造的に抑制されます（Provider実装がenum制約を厳密に遵守する場合。実際の遵守保証はProvider側の責務であり、本ADRはSchema契約を狭めるところまでが責務範囲です）。
- Starter Organization固有のRole名はCoreコードに一切ハードコードされておらず、任意のOrganization構成（異なるRole titleの集合）でも同じロジックがそのまま機能します。
- Organization rosterが変化する（Role追加・改名・Employee入れ替え）たびに、次回のCEO Plan生成でSchemaが自動的に再構築され、常に最新のRole集合を反映します。
- Organization rosterに使用可能なRoleが1つも存在しない場合、CEO Plan生成はProvider呼び出し前に確実に失敗します（`ErrNoAllowedRoles`、`CEOPlanPromptStage`）。これは新しい失敗モードですが、既存の`assignment_no_match`という発見しにくい失敗を、より早期の・より診断しやすい失敗へ置き換えるものです。
- `organization.ResolveTaskAssignment`・write fallback・ambiguity semantics・capability policyは今回一切変更されておらず、既存の回帰リスクはありません。
- `required_role`は将来のcapability-based assignment移行に伴うdeprecation candidateとして記録されました——削除は今回行いません。
