[English](CONTRIBUTING.md) | 日本語

# WorkCairnへのContributing

このguideは、WorkCairnへコードをcontributeする開発者向けです。製品としての利用方法は[README](README.ja.md)を、運用方法は[Operator Guide](docs/OperatorGuide.md)を参照してください。

## 作業前に

[AGENTS.md](AGENTS.md)、[docs/CONSTITUTION.md](docs/CONSTITUTION.md)、[docs/Architecture.md](docs/Architecture.md)を読んでください。このrepositoryの不変条件と現在の構造の正本です。対象領域に関連する決定がないか`docs/adr/`も確認してください。

## IssuesとDiscussions

- **Issues** — bug報告と具体的なfeature request。
- **Discussions** — 質問、アイデア、まだ具体的な提案になっていないもの。
- **Security報告** — 公開Issueへは書かず、[SECURITY.md](SECURITY.md)（GitHub Private Vulnerability Reporting）を使用してください。

## Development environment

- Go 1.23以上、POSIX shell、`make`、`tar`。
- build・testに実Vault、`.env`、実Provider API keyは不要です。
- Browser testにはNode.js 20以上が追加で必要です（test専用で、製品buildには含まれません）。

## BranchとCommit

`main`からbranchします。Commit messageは[Conventional Commits](https://www.conventionalcommits.org/)（`feat:`、`fix:`、`docs:`、`chore:`など）に従ってください。`CHANGELOG.md`はこの履歴から生成されるため、summary行は正確に、変更範囲へ対応させてください。

## BuildとTest

このrepositoryの`Makefile`に実在するcommandだけを載せています。推測でcommandを増やさず、sourceから確認してください。

```bash
make go-build                          # 3 binaryをbin/へbuild
cd go && go test -count=1 ./...        # unit test
cd go && go test -race -count=1 ./...  # race detector
cd go && go vet ./...
gofmt -l .                             # 出力が空であること
make public-beta-smoke                 # 高速なend-to-end smoke: Mock Provider + temporary Vault
make v1-release-gate                   # build + 全test + race + vet + gofmt + release matrix + git diff --check
```

UIを変更した場合は[Browser test](#browser-test)も参照してください。

## Architecture rules

詳細は[AGENTS.md](AGENTS.md)と[docs/CONSTITUTION.md](docs/CONSTITUTION.md)にあります。構造に関わる変更の前に必ず読んでください。要点は次のとおりです。

- **Go Only。** 製品コード、build、test、release toolingはGoです。唯一の例外はtest専用のPlaywright browser harness（ADR-0043）で、Go moduleや製品binaryへは入りません。
- **Kernelは調停役、Domainが判断する。** KernelはService登録とCommand調停に限定し、ビジネスルール、保存形式、Provider設定を持ちません。
- **Event駆動。** Business Eventはlog entryではなく事実です。Audit、Notification、MetricsはEventのsubscriberとして接続し、直接書き込みません。
- **Adapterは境界に置く。** Vault、filesystem、HTTP、LLM ProviderへのアクセスはすべてAdapterです。Coreはこれらへ依存しません。
- **Task lifecycleの所有者は1つ。** Task状態変更とTask lifecycle Eventの発行元は`TaskService`だけです。
- **RunnerはProvider呼び出しだけを行う。** Task状態、承認、retry、Audit、Deliverable保存には触れません。
- **副作用の前に明示承認。** 承認なしではTask開始、外部呼び出し、永続化のいずれも始まりません。
- **暗黙のretry・fallback禁止。** credential不足、timeout、状態不明はそのまま表面化させ、黙って再試行・別経路へ回しません。
- **既存primitiveを先に探す。** repository内、Go標準library、既に採用済みのdependencyの順で確認してから新規実装を検討します。
- **ビジネスルールはWorkCairn固有のまま保つ。** Task lifecycle、Approval、Recoveryなどの製品固有ロジックを外部libraryへ委譲しません。

## JSON Contract compatibility

JSON Contract v1（付随するPrompt／Markdown／migration fixtureを含む）は安定したlanguage-neutralな境界です。追加的で後方互換な変更を既定とし、破壊的な変更には新しいcontract version、migration plan、fixture更新、ADRが必要です。

## Secrets／Credentials

- `.env`や実credentialをcommitしない。
- 実API keyやcredentialをtest fixtureへ入れない。
- testはKeychainやheadless-local credential fileを読まない。Fake Providerを使う。
- testは実Provider APIを呼ばない。既存のFake Runner／Mock HTTP serverを使う。
- testは実Vaultへ接続しない。temporary directoryを使う。

## UI変更

`go/internal/httpapi/web/`を変更する場合、次を確認してください。

- Light modeとDark modeの両方が正しく表示されること（色は`styles.css`の既存CSS変数を参照し、hard-codeしない）。
- optionalな値が文字列として`null`／`undefined`をそのまま表示しないこと。
- 製品自体がplatform-neutralな箇所で、copyもplatform-neutralなままであること。
- 関連するbrowser testが成功すること（[Browser test](#browser-test)参照）。

個々のAI社員名など、現在Public UIで意図的に非表示にしている情報があります。変更で再表示することになる場合は、見落としと決めつけず、先に`docs/adr/`と直近のPublic Beta ADRで設計意図を確認してください。

## Browser test

`tests/browser/`はPlaywrightベースのtest専用harnessです（ADR-0043の境界は[AGENTS.md](AGENTS.md)参照）。初回だけ準備します。

```bash
make public-beta-browser-setup
```

その後は[AGENTS.md](AGENTS.md)の段階的検証に従います。

```bash
make check-ui-fast                # chromium-desktopのみ、@criticalタグだけ -- 実装中
make check-ui-changed AREA=<tag>  # chromium-desktopのみ、該当tag -- その領域の実装完了時
make check-ui-full                # Chromium＋WebKit iPhoneのフルsuite -- commit候補の直前に1回だけ
```

full suiteを実装のたびに回さないでください。変更の途中ではなく、変更の終わりに1回実行するためのものです。

## ADR

次のような変更にはADRが必要です: 新しいpersistent stateの追加、JSON Contractの変更、所有権の変更、retry／fallback semanticsの変更、Provider境界の変更、security境界の変更、storage architectureの変更。小さなbug fixには通常不要です。`docs/adr/ADR-template.md`から作成してください。Accepted ADRは、後のbrandingや用語変更に合わせて書き換えません。新しい判断は新しいADRとして記録します。

## Documentation

- [README.md](README.md)／[README.ja.md](README.ja.md)は一般利用者向けです。製品が何をするかを説明し、内部用語を持ち込みません。
- [docs/OperatorGuide.md](docs/OperatorGuide.md)は運用者向けで、deploymentと運用の詳細を扱います。
- [docs/Architecture.md](docs/Architecture.md)と`docs/adr/`はcontributor向けで、現在の構造と過去の設計判断を扱います。

利用手順を変更する場合は同じ変更内でREADME／Operator Guideを更新し、大きな設計判断であればADRとして記録してください。

## Pull request checklist

1. 変更理由と対象scopeを説明する。
2. 追加・変更した契約とfailure semanticsを明示する。
3. 関連するtest、race test、`go vet`、`gofmt`、`git diff --check`を成功させる。UIを変更した場合は関連browser testも実行する。
4. testのどこでも実API・実Vaultを使用していないことを確認する。
5. 差分にgenerated artifact、secret、local pathが含まれないことを確認する — `bin/`、`dist/`、`node_modules/`、`test-results/`、`.env`、Vault dataなど。詳細は`.gitignore`を参照。

## Release

tag作成、GitHub Release作成、`main`へのforce pushはmaintainerが行う操作です。contributorのPRで行うものではありません。
