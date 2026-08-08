package process

import (
	"context"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/organization"
)

// OrganizationInspection is a read-only projection of the legacy Python
// Organization and IdentityPolicy contracts. No repair or synchronization is
// attempted by this process.
type OrganizationInspection struct {
	Inventory        organization.Inventory         `json:"inventory"`
	ValidationIssues []organization.ValidationIssue `json:"validation_issues"`
	IdentityAudit    organization.IdentityAudit     `json:"identity_audit"`
}

func InspectOrganization(ctx context.Context, vaultRoot string) (OrganizationInspection, error) {
	if ctx == nil {
		return OrganizationInspection{}, fmt.Errorf("inspect Organization: context is required")
	}
	loader, err := vault.NewLoader(strings.TrimSpace(vaultRoot))
	if err != nil {
		return OrganizationInspection{}, fmt.Errorf("inspect Organization: %w", err)
	}
	inventory, err := loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return OrganizationInspection{}, fmt.Errorf("inspect Organization: %w", err)
	}
	return OrganizationInspection{
		Inventory: inventory, ValidationIssues: organization.ValidateInventory(inventory),
		IdentityAudit: organization.AuditIdentities(inventory, organization.DefaultSimilarityThreshold),
	}, nil
}

func ValidateIdentityName(ctx context.Context, vaultRoot, name string) (organization.NameValidation, error) {
	inspection, err := InspectOrganization(ctx, vaultRoot)
	if err != nil {
		return organization.NameValidation{}, err
	}
	names := make([]string, 0, len(inspection.Inventory.Identities))
	for _, identity := range inspection.Inventory.Identities {
		if identity.Name != "" {
			names = append(names, identity.Name)
		}
	}
	return organization.ValidateName(name, names, organization.DefaultSimilarityThreshold), nil
}
