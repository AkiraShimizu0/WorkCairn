# ADR-0033: Public Beta前にcompatibility distributionを廃止しrepositoryをGo Onlyにする

## Status

Accepted

## Context

ADR-0001以降、Workspace OSは段階的に製品責務をGoへ移し、通常運用、Interaction、Execution、Review、Revision、Organization、Recovery、Command Ledger、daemonまでをGo Only Runtimeとして成立させました。移行中は外部利用者を想定し、旧packageのimport surface、console entry point、Provider SDK、test、lockfileをcompatibility distributionとしてrepositoryに残していました。

Public Beta前の現在、旧packageを利用する外部callerは存在せず、互換distributionを公開した実績もありません。このsurfaceを残すと、clone後のtestとreleaseに2つのtoolchainが必要になり、製品の正式入口が曖昧になります。一方、JSON Contract v1、Prompt golden、Markdown、migration、E2E fixtureはGo testsが直接利用しており、旧Runtimeを残さず契約価値を維持できます。

## Decision

### Repository and distribution are Go Only

旧package source、compatibility／migration test、console entry point、package metadata、lockfile、Provider SDK、environment loader、専用build toolingをrepositoryから削除します。正式な製品surfaceは次です。

- `workspace-run`: CLIのplan／approval／execute／inspect／recovery
- `workspace-daemon`: HTTP、mobile-first Local Web UI、Scheduler lifecycle
- `workspace-core`: JSON Contract v1を提供するstorage-neutral process boundary

clone後のbuild、test、Release Gate、release archiveはGo toolchainだけで成立させます。`make v1-release-gate`を唯一の正式Release Gateとし、別Runtime向けGateやdependency restore手順を持ちません。

### Contracts survive the migration implementation

JSON Contract v1はPython専用契約として廃止せず、`workspace-core`の安定した外部process contractとして維持します。次のfixtureはGo testsが直接検証するlanguage-neutralな資産として残します。

- JSON Contract／Project／Workflow fixture
- Task／Review Prompt golden fixture
- Vault Markdown／managed metadata／migration fixture
- Organization／CEO plan／Interaction／Scheduler／Action／Recovery fixture

fixture名とtest名から移行元言語だけを示す表現は除きます。Prompt本文、Vault列、artifact schemaなど現行データ互換性に必要な内容は、出自だけを理由に変更しません。

### Historical ADRs remain historical

既存Accepted ADRが記録した移行Context、旧parserとの一時互換性、cutover理由は書き換えません。本ADRは、ADR-0001、0002、0006〜0019にある「移行完了後も公開compatibility APIを維持する」という現在状態の記述だけをsupersedeします。JSON Contract v1、Task／Event ownership、commit ordering、approval、partial failure、Recovery、Command Ledgerの判断はsupersedeしません。

既存Vaultを安全に読むための5列表、artifact filename、frontmatter、legacy metadata拒否／migration処理はデータ互換性であり、削除対象のpackage互換性とは分けて維持します。

### Public Beta gate

Release Gateは次を検査します。

- 3つのGo binary build
- 全Go test、race test、`go vet`、`gofmt`
- Architecture、Task Event ownership、approval、JSON Contract fixtureの既存gate
- repositoryにretired Runtime source、package metadata、lockfile、virtual environmentが存在しないこと
- release script syntax、差分whitespace

release archiveはallow-listされたGo binary、LICENSE、README、CHANGELOG、現在docsだけを含みます。実Vault、`.env`、secret、local build artifactは含めません。

## Consequences

- repository、build、test、release、distributionにGo toolchain以外のRuntimeは不要になります。
- 旧import path、console command、legacy classを利用するコードは動作しません。Public Beta前で外部callerがいないため、この破壊を受け入れます。
- 移行元実装との動的parity testはなくなりますが、同じ期待値をGo fixture／contract testが継続して固定します。
- Accepted ADR内の歴史的言及は残ります。現在構造はArchitecture、SystemOverview、Roadmap、本ADRを正とします。
