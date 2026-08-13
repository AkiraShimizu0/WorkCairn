package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const starterWorkspaceState = `---
updated_at: ""
---

# WorkCairn Workspace State

## Workspace Manager

| ID | 氏名 | 役割 | 状態 | 現在の作業 |
|---|---|---|---|---|

## 部署

| 部署 | 社員数 | 状態 |
|---|---:|---|
`

type WorkspaceLayoutResult struct {
	DirectoriesReady bool `json:"directories_ready"`
	StateReady       bool `json:"state_ready"`
	StateCreated     bool `json:"state_created"`
	EffectCommitted  bool `json:"effect_committed"`
}

// BootstrapWorkspaceLayout creates only the WorkCairn-owned layout below an
// already operator-selected root. It never discovers, selects, or mutates a
// different Obsidian Vault.
func BootstrapWorkspaceLayout(ctx context.Context, root string) (WorkspaceLayoutResult, error) {
	if ctx == nil || ctx.Err() != nil {
		return WorkspaceLayoutResult{}, fmt.Errorf("%w: context", ErrInvalidInput)
	}
	root = strings.TrimSpace(root)
	absolute, err := filepath.Abs(root)
	if err != nil {
		return WorkspaceLayoutResult{}, fmt.Errorf("%w: root", ErrInvalidInput)
	}
	if info, statErr := os.Stat(absolute); statErr != nil || !info.IsDir() {
		return WorkspaceLayoutResult{}, fmt.Errorf("%w: root", ErrDocumentNotFound)
	}
	result := WorkspaceLayoutResult{}
	for _, relative := range []string{"社員", "会社", "プロジェクト"} {
		path := filepath.Join(absolute, relative)
		_, beforeErr := os.Stat(path)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return result, fmt.Errorf("create Workspace layout: %w", err)
		}
		if errors.Is(beforeErr, os.ErrNotExist) {
			result.EffectCommitted = true
		}
	}
	result.DirectoriesReady = true
	statePath := filepath.Join(absolute, "会社", "Workspace State.md")
	content, err := readDocument(statePath, "Workspace State.md")
	if err == nil {
		for _, heading := range []string{"Workspace Manager", "部署"} {
			if _, sectionErr := markdownSectionLines(string(content), heading); sectionErr != nil {
				return result, sectionErr
			}
		}
		result.StateReady = true
		return result, nil
	}
	if !errors.Is(err, ErrDocumentNotFound) {
		return result, err
	}
	creator := osAtomicCreator{}
	if err := creator.Create(statePath, []byte(starterWorkspaceState), 0o644); err != nil {
		if !errors.Is(err, ErrAtomicTargetExists) {
			return result, err
		}
		content, readErr := readDocument(statePath, "Workspace State.md")
		if readErr != nil {
			return result, readErr
		}
		for _, heading := range []string{"Workspace Manager", "部署"} {
			if _, sectionErr := markdownSectionLines(string(content), heading); sectionErr != nil {
				return result, sectionErr
			}
		}
		result.StateReady = true
		return result, nil
	}
	result.StateReady, result.StateCreated = true, true
	result.EffectCommitted = true
	return result, nil
}
