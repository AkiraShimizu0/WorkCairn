package organization

import "sort"

func AuditIdentities(inventory Inventory, similarityThreshold float64) IdentityAudit {
	identities := inventory.Identities
	duplicateIDs := identityGroups(identities, func(identity Identity) string { return identity.ID })
	exactMatches := identityGroups(identities, func(identity Identity) string { return identity.Name })
	normalizedGroups := identityGroups(identities, func(identity Identity) string { return NormalizeName(identity.Name) })
	normalizedMatches := make([]IdentityGroup, 0, len(normalizedGroups))
	for _, group := range normalizedGroups {
		uniqueNames := make(map[string]struct{})
		for _, name := range group.Names {
			uniqueNames[name] = struct{}{}
		}
		if len(uniqueNames) > 1 {
			normalizedMatches = append(normalizedMatches, group)
		}
	}

	surnameGroups := make(map[string][]Identity)
	givenGroups := make(map[string][]Identity)
	invalidNames := make([]InvalidName, 0)
	for _, identity := range identities {
		if identity.Name == "" {
			continue
		}
		surname, givenName, valid := SplitJapaneseName(identity.Name)
		reasons := make([]string, 0, 2)
		if !valid {
			reasons = append(reasons, "invalid_name_format")
		}
		if len(findInvalidTerms(NormalizeName(identity.Name))) > 0 {
			reasons = append(reasons, "invalid_term")
		}
		if len(reasons) > 0 {
			invalidNames = append(invalidNames, InvalidName{
				EmployeeID: identity.ID, Name: identity.Name, IdentityType: identity.Type, Reasons: reasons,
			})
			continue
		}
		surnameGroups[surname] = append(surnameGroups[surname], identity)
		givenGroups[givenName] = append(givenGroups[givenName], identity)
	}
	sameGiven := namedIdentityGroups(givenGroups, "given_name")
	sameSurnames := namedIdentityGroups(surnameGroups, "surname")
	highSimilarity := similarityPairs(identities, similarityThreshold)

	issues := make([]AuditIssue, 0)
	appendIssues := func(issueType, level string, blocks bool, details []any) {
		for _, detail := range details {
			issues = append(issues, AuditIssue{Type: issueType, Level: level, BlocksHire: blocks, Details: detail})
		}
	}
	appendIssues("duplicate_id", "error", true, groupsAsAny(duplicateIDs))
	appendIssues("exact_match", "error", true, groupsAsAny(exactMatches))
	appendIssues("normalized_match", "error", true, groupsAsAny(normalizedMatches))
	appendIssues("invalid_name", "error", true, invalidNamesAsAny(invalidNames))
	appendIssues("same_given_name", "warning", true, namedGroupsAsAny(sameGiven))
	appendIssues("same_surname", "warning", false, namedGroupsAsAny(sameSurnames))
	appendIssues("high_similarity", "warning", false, similaritiesAsAny(highSimilarity))
	errors := make([]AuditIssue, 0)
	warnings := make([]AuditIssue, 0)
	for _, issue := range issues {
		if issue.Level == "error" {
			errors = append(errors, issue)
		} else if issue.Level == "warning" {
			warnings = append(warnings, issue)
		}
	}
	repairs := repairCandidates(exactMatches, normalizedMatches, sameGiven, invalidNames)
	retained := retainedIdentities(sameGiven)
	managerCount, reservedCount := 0, 0
	for _, identity := range identities {
		switch identity.Type {
		case IdentityWorkspaceManager:
			managerCount++
		case IdentityReserved:
			reservedCount++
		}
	}
	return IdentityAudit{
		EmployeeCount: len(inventory.Employees), IdentityCount: len(identities),
		WorkspaceManagerCount: managerCount, ReservedIdentityCount: reservedCount,
		DuplicateIDs: duplicateIDs, ExactMatches: exactMatches, NormalizedMatches: normalizedMatches,
		SameGivenNames: sameGiven, SameSurnames: sameSurnames, HighSimilarityNames: highSimilarity,
		InvalidNames: invalidNames, Issues: issues, Errors: errors, Warnings: warnings,
		RepairCandidates: repairs, RecommendedRenameTargets: cloneRepairCandidates(repairs),
		RecommendedRetainedIdentities: retained,
	}
}

func identityGroups(identities []Identity, key func(Identity) string) []IdentityGroup {
	groups := make(map[string][]Identity)
	for _, identity := range identities {
		value := key(identity)
		if value != "" {
			groups[value] = append(groups[value], identity)
		}
	}
	keys := make([]string, 0)
	for value, group := range groups {
		if len(group) > 1 {
			keys = append(keys, value)
		}
	}
	sort.Strings(keys)
	result := make([]IdentityGroup, 0, len(keys))
	for _, value := range keys {
		group := groups[value]
		names := make([]string, 0, len(group))
		summaries := make([]IdentitySummary, 0, len(group))
		for _, identity := range group {
			names = append(names, identity.Name)
			summaries = append(summaries, identitySummary(identity))
		}
		result = append(result, IdentityGroup{Key: value, Names: names, Employees: summaries})
	}
	return result
}

func namedIdentityGroups(groups map[string][]Identity, key string) []NamedIdentityGroup {
	values := make([]string, 0)
	for value, group := range groups {
		if len(group) > 1 {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	result := make([]NamedIdentityGroup, 0, len(values))
	for _, value := range values {
		identities := groups[value]
		names := make([]string, 0, len(identities))
		summaries := make([]IdentitySummary, 0, len(identities))
		for _, identity := range identities {
			names = append(names, identity.Name)
			summaries = append(summaries, identitySummary(identity))
		}
		result = append(result, NamedIdentityGroup{Key: key, Value: value, Names: names, Employees: summaries})
	}
	return result
}

func similarityPairs(identities []Identity, threshold float64) []SimilarityPair {
	if threshold <= 0 {
		threshold = DefaultSimilarityThreshold
	}
	result := make([]SimilarityPair, 0)
	for index, identity := range identities {
		for _, other := range identities[index+1:] {
			if NormalizeName(identity.Name) == NormalizeName(other.Name) {
				continue
			}
			score := Similarity(identity.Name, other.Name)
			if score >= threshold {
				result = append(result, SimilarityPair{
					Employees: []IdentitySummary{identitySummary(identity), identitySummary(other)},
					Names:     []string{identity.Name, other.Name}, Score: score,
				})
			}
		}
	}
	return result
}

func repairCandidates(exact, normalized []IdentityGroup, sameGiven []NamedIdentityGroup, invalid []InvalidName) []RepairCandidate {
	type candidateKey struct{ id, name string }
	candidates := make(map[candidateKey]*RepairCandidate)
	add := func(identity IdentitySummary, reason, action string) {
		key := candidateKey{identity.ID, identity.Name}
		candidate, exists := candidates[key]
		if !exists {
			candidate = &RepairCandidate{EmployeeID: identity.ID, Name: identity.Name, Reasons: []string{}, SuggestedAction: action}
			candidates[key] = candidate
		}
		for _, existing := range candidate.Reasons {
			if existing == reason {
				return
			}
		}
		candidate.Reasons = append(candidate.Reasons, reason)
	}
	for _, group := range append(append([]IdentityGroup{}, exact...), normalized...) {
		for _, identity := range renameTargets(group.Employees) {
			add(identity, "既存社員と同一視される名前", "重複しない日本語姓名へ変更する")
		}
	}
	for _, group := range sameGiven {
		for _, identity := range renameTargets(group.Employees) {
			add(identity, "既存社員と同じ名", "未使用の自然な名へ変更する")
		}
	}
	for _, invalidName := range invalid {
		add(IdentitySummary{ID: invalidName.EmployeeID, Name: invalidName.Name, Type: invalidName.IdentityType}, "姓名形式または使用語が不正", "自然な日本語の『姓 名』へ変更する")
	}
	result := make([]RepairCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, *candidate)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Name != result[right].Name {
			return result[left].Name < result[right].Name
		}
		return result[left].EmployeeID < result[right].EmployeeID
	})
	return result
}

func retainedIdentities(groups []NamedIdentityGroup) []RetainedIdentity {
	result := make([]RetainedIdentity, 0, len(groups))
	for _, group := range groups {
		ordered := append([]IdentitySummary(nil), group.Employees...)
		sort.Slice(ordered, func(left, right int) bool { return identitySummaryLess(ordered[left], ordered[right]) })
		if len(ordered) > 0 {
			identity := ordered[0]
			result = append(result, RetainedIdentity{
				ID: identity.ID, Name: identity.Name, Type: identity.Type, Source: identity.Source,
				Reason: group.Value + "の基準Identityとして維持",
			})
		}
	}
	return result
}

func renameTargets(group []IdentitySummary) []IdentitySummary {
	ordered := append([]IdentitySummary(nil), group...)
	sort.Slice(ordered, func(left, right int) bool { return identitySummaryLess(ordered[left], ordered[right]) })
	if len(ordered) < 2 {
		return []IdentitySummary{}
	}
	return ordered[1:]
}

func identitySummaryLess(left, right IdentitySummary) bool {
	priority := func(identityType IdentityType) int {
		switch identityType {
		case IdentityReserved:
			return 0
		case IdentityWorkspaceManager:
			return 1
		case IdentityEmployee:
			return 2
		default:
			return 3
		}
	}
	if priority(left.Type) != priority(right.Type) {
		return priority(left.Type) < priority(right.Type)
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return left.ID < right.ID
}

func identitySummary(identity Identity) IdentitySummary {
	return IdentitySummary{ID: identity.ID, Name: identity.Name, Type: identity.Type, Source: identity.Source}
}

func groupsAsAny(groups []IdentityGroup) []any {
	result := make([]any, len(groups))
	for index := range groups {
		result[index] = groups[index]
	}
	return result
}
func namedGroupsAsAny(groups []NamedIdentityGroup) []any {
	result := make([]any, len(groups))
	for index := range groups {
		result[index] = groups[index]
	}
	return result
}
func invalidNamesAsAny(values []InvalidName) []any {
	result := make([]any, len(values))
	for index := range values {
		result[index] = values[index]
	}
	return result
}
func similaritiesAsAny(values []SimilarityPair) []any {
	result := make([]any, len(values))
	for index := range values {
		result[index] = values[index]
	}
	return result
}
func cloneRepairCandidates(source []RepairCandidate) []RepairCandidate {
	result := make([]RepairCandidate, len(source))
	for index, candidate := range source {
		result[index] = candidate
		result[index].Reasons = append([]string(nil), candidate.Reasons...)
	}
	return result
}
