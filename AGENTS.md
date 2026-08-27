# AGENTS.md

このリポジトリで作業するAIエージェントは、次の規則に従ってください。このファイルはリポジトリ全体へ適用します。

## Start Here

作業前に、依頼に関係する範囲で次を確認します。

1. `.ai/workspace.md`
2. `docs/CONSTITUTION.md`
3. `docs/Architecture.md`
4. 関連する`docs/adr/ADR-*.md`
5. `docs/ROADMAP.md`
6. `git status`と直近のコミット

既存の未コミット変更はユーザーの作業として扱い、無断で破棄・巻き戻し・混在させません。

## Architecture Rules

- 製品コード、build、test、release、distributionはGo Onlyです。中核ビジネスルールはGoへ実装します。
- ADR-0043のPublic Beta Browser Acceptance harnessだけはtest-only Node／Playwrightを許可します。Go module、製品Runtime、release archive、通常の`v1-release-gate`へ混入させず、browser専用Gateとして分離します。
- repositoryへ別言語Runtime、legacy implementation、暗黙fallbackを再導入しません。
- KernelはService登録、ライフサイクル、Command調停に限定し、DomainロジックやProvider固有処理を持たせません。
- DomainはObsidian、Markdown、外部LLM SDK、`.env`、APIキーを知りません。
- ServiceはDomainとportを調停し、Adapterの保存形式へ依存しません。
- RunnerはTask状態、承認、retry、Audit、Deliverable保存を知りません。
- Task状態変更とTask lifecycle Event発行はTaskServiceだけが行います。
- Audit、Notification、MetricsはEvent subscriberとして接続し、Domain Serviceから直接書き込みません。

## Contracts and Compatibility

- JSON Contract v1を壊しません。変更はoptionalかつadditiveを優先し、破壊的変更は新versionとADRを必要とします。
- JSON Contract v1、Prompt／Markdown／migration fixtureをlanguage-neutralな契約として維持します。
- エラーは機械判定可能な型・code・stageへ変換し、Providerの生エラーを公開契約へ漏らしません。
- Event Typeは閉じた型とし、Task Eventを複数コンポーネントから二重発行しません。

## Safety and Data

- 実Vault、`.env`、APIキー、社員Markdown、実Projectデータを変更しません。ただし依頼が明示し、対象とバックアップが確認できた場合を除きます。
- 実LLM APIをテストや確認目的で呼びません。Fake RunnerまたはMockを使用します。
- 状態変更には明示的承認を要求し、dry-runがある場合は先に利用します。
- ファイル更新は一時ファイル・原子的置換・rollback可能な設計を優先します。
- IDを永続参照とし、氏名などの表示情報でTaskやEventを関連付けません。
- 秘密情報の値・一部・長さ・fingerprintを出力しません。

## Implementation Workflow

1. 変更前の状態、既存テスト、関連コード、ADRを確認する。
2. 最小のDomain／Service／Adapter境界を決める。新しい重大判断はADRへ記録する。
3. Fake、in-memory、temporary directoryを使って先にテスト可能な境界を作る。
4. 正常系に加え、拒否、partial failure、timeout、cancellation、並行実行をリスクに応じてテストする。
5. `gofmt`、`go vet`、race test、fixture contract testを変更範囲に応じて実行する。
6. `git diff --check`と`git status`で、対象外・機密・生成物の差分がないことを確認する。
7. コミットやpushは依頼された場合だけ行う。push禁止の依頼ではローカルコミットまでに留める。

## Standard Commands

```bash
make go-build
cd go && go test -count=1 ./...
cd go && go test -race -count=1 ./...
cd go && go vet ./...
make v1-release-gate
make public-beta-smoke
```

Go生成物`bin/workcairn-core`、`bin/workcairn`、`bin/workcairn-daemon`はGit管理しません。テストは実Vaultや実APIへ接続しない構成にしてください。

## Browser Gate Validation Staging

`make public-beta-browser-gate`（Chromium desktop + WebKit iPhone、フルsuite）は正しい検証ですが、実装途中で毎回回すには重すぎます。Test Speed roundで、`tests/browser/`を機能別spec（`conversation`/`deliverable`/`archive`/`setup`/`ai-office`/`failure`/`detail-pane`/`mobile-layout`）へ分割し、`@critical`/`@conversation`/`@deliverable`/`@archive`/`@setup`/`@office`/`@failure`/`@detail`/`@mobile`タグを付与しました。UI変更時は次の3段階で検証してください。

1. **実装中**: `make check-ui-fast`（chromium-desktopのみ、`@critical`タグだけ、数分以内）を、変更のたびに繰り返します。full Browser Gateを実装途中で繰り返しません。
2. **関連実装完成時**: 触れたUI領域に対応するtagで`make check-ui-changed AREA=<tag>`（例: `AREA=deliverable`）を実行し、Green化してから次の作業へ進みます。
3. **Checkpoint完成・commit候補時**: `make check-ui-full`（`public-beta-browser-gate`と同じ、Chromium＋WebKit両方のフルsuite）を1回だけ実行し、その後`go test -race ./...`と`make v1-release-gate`も実行します。

full Browser Gateでfailureが出た場合は、失敗したtestだけを修正・再実行し、原因を潰してから最後にfullをもう一度だけ実行します。full suiteを失敗のたびに繰り返しません。

新しいUI testを追加する際は、実際にbrowser固有の確認（DOM描画、table、mobile overflow、composer pinned等）が必要な場合だけPlaywrightへ追加し、Markdown parserやrole labelのような純粋なmapping/formatting ロジックは（unit test基盤を追加する際は）より高速な層で検証することを優先してください。

## AI Code Minimality

このセクションはArchitecture Rule（上記）を置き換えません。生成AIがコードを書く前後で従うprocess disciplineだけを追加します。

- **書く前**: (1) repository内に既存primitiveがないか検索する、(2) Go標準libraryで解決できないか、(3) 既に採用済みのdependencyで解決できないか、(4) 成熟した外部libraryが妥当か、(5) それでも必要な場合だけWorkCairn固有実装を書く。ただしTask lifecycle、Approval、LoopGuard、Progress Intelligence、BudgetGuard、Recovery、Dependency Evidence、Synthesis QualityなどのWorkCairn固有Business Ruleは外部libraryへ丸投げしません。
- **設計中**: 最小限の抽象化に留め、同じ事実を複数箇所へ重複させません（Single Source of Truth）。interface／factory／manager／registryは、複数実装または複数呼び出し元が実在する場合にだけ追加します。1実装・1呼び出し元ならpure functionやsmall structで足りないか検討します。将来使うかもしれない拡張点を先回りして作りません。
- **変更後・Checkpoint前**: 新方式に置き換えられた旧実装が残っていないか、同じvalidationやduplicate stateが複数箇所に増えていないか、dead helper／exportが残っていないか、simplifyできないか、不要なdependencyを増やしていないかを確認します。

## Documentation

- 現在の構造は`docs/Architecture.md`、規範は`docs/CONSTITUTION.md`、計画は`docs/ROADMAP.md`を正とします。
- 重大な設計判断は`docs/adr/ADR-template.md`からADRを作成します。
- 実装完了時は、現在の責務境界、互換契約、次の開発候補を明記します。
- README、Architecture、ADR、Starter Kit間で事実や用語が矛盾しないようにします。
