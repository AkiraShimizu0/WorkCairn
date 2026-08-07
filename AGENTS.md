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

- 最終実装はGo 100%、Python 0%です。新しい中核ビジネスルールはGoへ実装します。
- Pythonは移行期間中のlegacy/reference、Vault Adapter、Prompt、Provider Runner、LLM SDKに限定します。暗黙のPython fallbackは禁止です。
- KernelはService登録、ライフサイクル、Command調停に限定し、DomainロジックやProvider固有処理を持たせません。
- DomainはObsidian、Markdown、外部LLM SDK、`.env`、APIキーを知りません。
- ServiceはDomainとportを調停し、Adapterの保存形式へ依存しません。
- RunnerはTask状態、承認、retry、Audit、Deliverable保存を知りません。
- Task状態変更とTask lifecycle Event発行はTaskServiceだけが行います。
- Audit、Notification、MetricsはEvent subscriberとして接続し、Domain Serviceから直接書き込みません。

## Contracts and Compatibility

- JSON Contract v1を壊しません。変更はoptionalかつadditiveを優先し、破壊的変更は新versionとADRを必要とします。
- Python／Go共有fixtureを契約として維持します。
- 公開済みPython APIは、移行対象として明示されるまで互換性を維持します。
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
5. `gofmt`、`go vet`、race test、Python互換テスト、fixtureを変更範囲に応じて実行する。
6. `git diff --check`と`git status`で、対象外・機密・生成物の差分がないことを確認する。
7. コミットやpushは依頼された場合だけ行う。push禁止の依頼ではローカルコミットまでに留める。

## Standard Commands

```bash
make go-build
cd go && go test ./...
cd go && go test -race ./...
cd go && go vet ./...
PYTHONDONTWRITEBYTECODE=1 .venv/bin/python -m unittest discover -s tests
PYTHONPYCACHEPREFIX=/tmp/workspace-os-pycache .venv/bin/python -m compileall -q src tests
```

Go生成物`bin/workspace-core`はGit管理しません。テストは実Vaultや実APIへ接続しない構成にしてください。

## Documentation

- 現在の構造は`docs/Architecture.md`、規範は`docs/CONSTITUTION.md`、計画は`docs/ROADMAP.md`を正とします。
- 重大な設計判断は`docs/adr/ADR-template.md`からADRを作成します。
- 実装完了時は、移行済み責務、残存Python責務、互換性、次の移行候補を明記します。
- README、Architecture、ADR、Starter Kit間で事実や用語が矛盾しないようにします。
