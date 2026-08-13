# ADR-0039: CEO PlanをLLM Intent + Go Normalizerで構築する

## Status

Accepted

## Context

ADR-0019以降、CEO Plan生成はGoのProvider-neutral Serviceと既存Runnerを通り、strict JSON parserとGo Domain validationがLLM出力を検証してきました。直前のCheckpointではAnthropic Structured Outputsを導入し、その出力の整形をさらに安定させました。

しかし実機Acceptanceでは、`json_decode_failed`、`missing_required_field`、不正なassignment・dependency、Review `marker_missing`など、LLM出力contract境界に起因する失敗が繰り返し発生しました。根本原因はStructured Outputsで緩和できる「JSON整形の不安定さ」ではなく、CEO PlanのLLMに

- 意味理解（何をすべきか）
- Project構造・Task構造の決定
- Employee assignment（誰が担当するか）
- Dependency（どのTaskがどのTaskに依存するか）
- 正式ID、必須field、Canonical JSON shapeの遵守

という異なる種類の責務を同時に負わせていたことにあります。特に`ceoplan.NormalizeCandidate`の既存コードを調査した結果、`organization.ResolveTaskAssignment`の呼び出し後に`AssignmentResult.Status`を一切確認していない**latent bug**が見つかりました。Statusが`AssignmentNoMatch`または`AssignmentAmbiguous`のとき`EmployeeID`は`nil`ですが、コードはそのまま「未割当」として処理を続行し、割当の曖昧さを一度も安全に拒否していませんでした。

## Decision

CEO Plan生成の構造を次のように変更します。

```text
User Request
  → LLM (Structured Outputs: small Intent schema)
  → ceoplan.Intent (project_name / objective / summary / steps[] / ceo_questions)
  → ceoplan.ParseIntent          (JSON構造のみ検証。既存ParseRunnerOutputと同じ分離)
  → ceoplan.NormalizeIntent      (Go Normalizer — 新設)
      - organization.ResolveTaskAssignment を再利用してrequired_roleごとにEmployeeを解決
      - proposed_tasksのdependency_idsをstep順から線形に構築（LLMは書かない）
      - required_departments / assigned_existing_employeesを解決結果から導出
      - 組み立てたcandidatePlanを既存 ceoplan.NormalizeCandidate へそのまま渡す
  → ceoplan.Plan（既存Canonical Contract、無変更）
  → 既存のApproval / Apply / Interaction / Workflow（無変更）
```

### LLM Intentの責務

`ceoplan.Intent`は意図的に小さく、LLMにしか判断できないsemantic informationだけを残します。

- `project_name` / `objective` / `summary` — 何を目指すか（display用のproject_nameを含む。storage identityはapply時の既存policyが別途決定）
- `steps[].kind` — `write` / `research` / `analyze` / `implement` / `review` の5値enum。未知kindは推測せず`unknown_step_kind`として安全拒否
- `steps[].description` — そのstepで何を行うか
- `steps[].required_role` — 誰に必要なRoleか（社員IDではなく、会社のRole語彙に合わせた文字列。review kindでは省略可）
- `ceo_questions` — LLM自身が識別した本質的な曖昧さ（assignmentの曖昧さはGoが決定的に処理するため含まない）

Employee ID、Task ID、Project ID、canonical dependency ID、`required_departments`/`required_roles`/`assigned_existing_employees`配列、`missing_roles`、`plan_only`は一切含めません。

`kind: "review"`のstepはproposed_tasksへ変換されません。既存Reviewed Workflow（ADR-0024）がすべてのTask後に自動でReviewを実行するため、CEO Planレベルの明示的な「Reviewタスク」は独立した意味を持たないためです。dependency chainの構築ではこのstepを読み飛ばし、次の実stepは直前の実stepにだけ依存します。

### Go Normalizerの責務

`ceoplan.NormalizeIntent`がGoへ移した責務です。

- Employee assignment解決 — 既存`organization.ResolveTaskAssignment`をrequired_roleだけで呼び出し（LLMはProposedEmployeeIDを提案しない）。`AssignmentResolved`のみ採用し、`AssignmentNoMatch`／`AssignmentAmbiguous`は`ceoplan.NormalizationError`として型付きで安全拒否する。これが前述のlatent bugの修正でもある
- Dependency構築 — step順による線形chain。LLM由来のdependency IDが存在しないため、循環は構造的に発生し得ない
- `required_departments` / `assigned_existing_employees`の導出 — 解決済みAssigneeのDepartment／IDから重複排除して構築
- Canonical識別子・validation — 組み立てたcandidatePlanを既存の**未変更**`ceoplan.NormalizeCandidate`へ渡し、proposal ID採番、`required_roles`自動収集、dependency cycle検査、project name安全性検査などの既存business ruleをそのまま再利用する（重複実装しない）

### Diagnostics

新しいsanitized stage/reasonを追加しました。rawなProvider応答は一切保存しません。

| Stage | 意味 | Reason例 |
|---|---|---|
| `ceo_plan_intent` | Intent JSONが不正、または未知kind | `json_decode_failed`, `unknown_step_kind`, `missing_required_field` |
| `ceo_plan_normalization` | Go側でassignmentを決定できない | `assignment_no_match`, `assignment_ambiguous` |
| `ceo_plan_parser` | Go構築済みcandidateの最終canonical validation（defense-in-depth。既存reasonのまま） | 既存`ceoplan.ParseFailureReason`一式 |

`dependency_resolution`という独立stageは設けていません。dependencyはstep順から構造的に決定されるため循環や不整合が原理的に発生せず、独立した失敗stageを持たせると到達しない分岐を作ることになるためです（既存`validateDependencyGraph`の循環検査は防御的にそのまま残しています）。

### Assignment曖昧時の挙動（rejected alternative）

必要Roleに合致する社員が0件または複数件の場合、動的にInteraction Clarification（`ceo_questions`経由の質問生成）へ接続する案も検討しましたが、今回は**typed failureとして安全拒否**する方を採用しました。既存のClarification loopは「LLMが認識した曖昧さ」を扱う設計であり、「Go側で決定的に判明したassignmentの曖昧さ」をそこへ接続するには新しいClarification生成ロジックとInteraction Sessionとの結線が追加で必要になり、今回のscopeを大きく広げるためです。CEOはRoleを見直すか、Organizationを調整して再依頼します。将来、この2つの曖昧さの扱いを統合したい場合は別ADRで設計します。

### Structured Outputsの位置づけの変更

用途を「巨大なCanonical PlanをLLMに強制生成させる」から「小さいIntent Contractを安定して受け取る」へ変更しました。`ceoplan.OutputJSONSchema()`（前Checkpointで追加したCanonical Plan用schema）は使用箇所がなくなったため削除し、`ceoplan.IntentJSONSchema()`に置き換えています。Provider固有の`output_config.format`変換は引き続き`adapter/claude`だけに閉じ込めており、Core／DomainはAnthropic固有仕様を一切知りません。

### Canonical JSON Contractの維持

`ceoplan.Plan`／`ceoplan.ProposedTask`の型・JSON shapeは無変更です。Interaction Session、Command payload契約、CEO Plan Apply pipeline、既存fixtureとその契約testはすべてそのまま通用します。「Canonical JSONの生成者」がLLMからGo Normalizerへ変わっただけで、Canonical Contract自体は変えていません。

### Model／Provider更新耐性

Intent Contract、Go Normalizer、Canonical Contractという3層境界はProvider／model非依存です。Claude以外のモデルやProviderへ切り替えても、Intent JSON Schemaを満たす応答さえ返せればGo Normalizerと既存Canonical validationはそのまま機能します。Provider固有のDomain logicはこのIntent移行後もAdapter層だけに存在します。

### Migration / Backward Compatibility

Big Bang rewriteを避け、次の段階でCEO Planだけを対象に完了させました。

1. Intent Contract追加（`ceoplan.Intent`, `ParseIntent`）
2. Go Normalizer追加（`ceoplan.NormalizeIntent`、既存`NormalizeCandidate`を再利用）
3. CEO Plan generationをIntent経由へ変更（`CEOPlanService.Generate`の内部実装のみ変更。`CEOPlanInput`/`CEOPlanResult`は無変更）
4. 既存Canonical Plan以降（Approval / Apply / Interaction / Workflow）は無変更のまま維持

Reviewの同じ発想での再設計（human Markdownを排したReview Intent `{verdict, issues}`とGo/UI側でのMarkdown projection化）は、既存JSON Contract・canonical Review artifactとの互換性検討が必要なため、今回は実装せず次のCheckpointへ段階移行します。旧Prompt依存の削減は、CEO Plan Promptを丸ごとIntent向けに書き換えたことで副次的に達成されました。

## Consequences

- LLMの責務は意味理解と作業方針の提案に限定され、Employee ID、Task ID、Dependency IDの推測が構造的になくなりました。
- 曖昧なassignmentは推測されず、常に型付き失敗として観測可能です（latent bugの修正）。
- Dependencyの循環は構造的に発生し得なくなりました。
- Canonical CEO Plan JSON Contractは既存consumer（Interaction、Command payload、Apply pipeline、fixture）から見て不変です。
- Structured Outputsは「小さく安定した契約を受け取る」ための手段として再定義され、Provider固有処理はAdapter境界に留まっています。
- Reviewの同等移行、および動的Clarificationへのassignment曖昧性接続は将来の別Checkpoint／ADRへ持ち越しました。
