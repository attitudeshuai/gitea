// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package audit

import (
	"context"
	"math/rand/v2"

	audit_model "gitea.dev/models/audit"
	access_model "gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/setting"
)

var permissionDenyActions = map[access_model.DenyReason]audit_model.Action{
	access_model.DenyReasonUnauthenticated:        audit_model.RepositoryPermissionDenyUnauthenticated,
	access_model.DenyReasonOwnerInvisible:         audit_model.RepositoryPermissionDenyOwnerInvisible,
	access_model.DenyReasonOrgMembershipRequired:  audit_model.RepositoryPermissionDenyOrgMembershipRequired,
	access_model.DenyReasonTeamUnitNotGranted:     audit_model.RepositoryPermissionDenyTeamUnitNotGranted,
	access_model.DenyReasonRepoUnitRestricted:     audit_model.RepositoryPermissionDenyRepoUnitRestricted,
	access_model.DenyReasonCredentialInsufficient: audit_model.RepositoryPermissionDenyCredentialInsufficient,
}

// randomFloat64 is overridable in tests to make sampling deterministic.
var randomFloat64 = rand.Float64

// RecordPermissionDeny records d, the decision for a denied request on repo, subject to the
// permission-deny switch and sample rate. Recording must never change the served response.
func RecordPermissionDeny(ctx context.Context, repo *repo_model.Repository, doer *user_model.User, d *access_model.Decision) {
	if !setting.PermissionDenyEnabled() {
		return
	}
	action, ok := permissionDenyActions[d.DenyReason]
	if !ok {
		return
	}
	rate := setting.PermissionDenySampleRate()
	if rate <= 0 {
		return
	}
	if rate < 1 && randomFloat64() >= rate {
		return
	}
	// anonymous denials are ordinary events (actor ID 0), not a missing-actor error
	actor := audit_model.EntityRef{Type: audit_model.ScopeUser}
	if doer != nil {
		actor = actorRef(doer)
	}
	persistEvent(ctx, RecordParams{
		Action:          action,
		Actor:           actor,
		ActorCredential: actorCredential(ctx, doer),
		Scope:           scopeRef(repo),
		Metadata: map[string]any{
			"unit":   d.Unit.Name(),
			"source": string(d.Source),
		},
	})
}
