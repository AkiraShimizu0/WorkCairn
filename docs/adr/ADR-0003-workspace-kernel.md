# ADR-0003: Workspace Kernelを中心コンポーネントとする

## Status

Accepted

## Context

Workspace OSにはWorkflowEngine、Scheduler、Organization、Project、Task、監査、外部Adapterなど、会社全体の状態と処理を調整する複数のコンポーネントがあります。各コンポーネントが相互に直接依存すると、起動順序、状態管理、イベント配送、障害処理が分散し、長期的な拡張が難しくなります。

## Decision

Workspace KernelをWorkspace OSの中心コンポーネントとします。KernelはGoで実装し、WorkflowEngine、Scheduler、Organization、Project/Taskドメイン、監査イベントをKernel配下のサービスとして管理します。

Kernelはサービスのライフサイクル、依存関係、コマンド受付、イベント配送、実行状態を調整します。Obsidian、CLI、API、LLM RunnerなどはKernelの外側にあるAdapterとして接続します。初期段階ではKernelは設計上の境界であり、既存コンポーネントを段階的に統合します。

## Consequences

- 会社全体の起動、停止、状態、イベントを一貫した境界で管理できます。
- WorkflowEngineやSchedulerはKernelの管理対象となり、相互の直接依存を減らせます。
- Kernel自体が巨大なビジネスロジック層にならないよう、ドメインサービスとの責務分離が必要です。
- Pythonコンポーネントは移行期間中、KernelへJSON Contractなどで接続するAdapterになります。
- Kernel API、コマンド、イベント形式について追加のADRが必要になります。
