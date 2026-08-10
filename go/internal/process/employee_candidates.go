package process

import (
	"context"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

func ValidateEmployeeCandidates(ctx context.Context, root string, candidates []organization.EmployeeCandidate) ([]organization.CandidateValidation, error) {
	loader, err := vault.NewLoader(root)
	if err != nil {
		return nil, err
	}
	inventory, err := loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return nil, err
	}
	return organization.ValidateCandidates(inventory, candidates)
}
