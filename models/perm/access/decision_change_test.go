// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package access

import (
	"context"
	"testing"

	"gitea.dev/models/db"
	"gitea.dev/models/organization"
	perm_model "gitea.dev/models/perm"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/cache"
	"gitea.dev/modules/cachegroup"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func explain(ctx context.Context, t *testing.T, repo *repo_model.Repository, user *user_model.User, tp unit.Type) *Decision {
	t.Helper()
	d, err := ExplainDoerRepoPermission(ctx, repo, user, tp)
	require.NoError(t, err)
	return d
}

func TestExplainStableReproducible(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 39}) // private limited_org

	first := explain(ctx, t, repo, user4, unit.TypeCode)
	for range 3 {
		d := explain(ctx, t, repo, user4, unit.TypeCode)
		assert.Equal(t, first, d)
	}
}

func TestExplainTeamMembershipChange(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 39}) // private limited_org id 22

	d := explain(ctx, t, repo, user4, unit.TypeCode)
	assert.Equal(t, DenyReasonOrgMembershipRequired, d.DenyReason)

	// becoming an org member (owners team, not linked to this repo) changes the decisive reason
	require.NoError(t, db.Insert(ctx,
		&organization.OrgUser{OrgID: 22, UID: user4.ID},
		&organization.TeamUser{OrgID: 22, TeamID: 15, UID: user4.ID},
	))
	d = explain(ctx, t, repo, user4, unit.TypeCode)
	assert.Equal(t, DenyReasonTeamUnitNotGranted, d.DenyReason)
	assert.Equal(t, DecisionSourceTeamUnitMode, d.Source)

	// repeatable in the new state
	assert.Equal(t, d, explain(ctx, t, repo, user4, unit.TypeCode))

	// leaving restores the previous conclusion
	_, err := db.DeleteByBean(ctx, &organization.OrgUser{OrgID: 22, UID: user4.ID})
	require.NoError(t, err)
	_, err = db.DeleteByBean(ctx, &organization.TeamUser{OrgID: 22, TeamID: 15, UID: user4.ID})
	require.NoError(t, err)
	d = explain(ctx, t, repo, user4, unit.TypeCode)
	assert.Equal(t, DenyReasonOrgMembershipRequired, d.DenyReason)
}

func TestExplainCollaboratorChange(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2}) // private individual repo

	d := explain(ctx, t, repo, user4, unit.TypeCode)
	assert.Equal(t, DenyReasonRepoUnitRestricted, d.DenyReason)

	require.NoError(t, db.Insert(ctx, &repo_model.Collaboration{RepoID: repo.ID, UserID: user4.ID, Mode: perm_model.AccessModeRead}))
	require.NoError(t, RecalculateUserAccess(ctx, repo, user4.ID))
	d = explain(ctx, t, repo, user4, unit.TypeCode)
	assert.True(t, d.Allowed)
	assert.Equal(t, DecisionSourceCollaborator, d.Source)

	_, err := db.DeleteByBean(ctx, &repo_model.Collaboration{RepoID: repo.ID, UserID: user4.ID})
	require.NoError(t, err)
	require.NoError(t, RecalculateUserAccess(ctx, repo, user4.ID))
	d = explain(ctx, t, repo, user4, unit.TypeCode)
	assert.Equal(t, DenyReasonRepoUnitRestricted, d.DenyReason)
}

func TestExplainIgnoresPermissionCache(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 39})

	ctx := cache.WithCacheContext(t.Context())
	c := cache.GetContextCache(ctx)
	// pre-seed a stale, fully-allowed permission for this pair
	c.Put(cachegroup.RepoUserPermission, RepoUserPermissionCacheKey(repo.ID, user4), Permission{AccessMode: perm_model.AccessModeOwner})

	d := explain(ctx, t, repo, user4, unit.TypeCode)
	assert.False(t, d.Allowed)
	assert.Equal(t, DenyReasonOrgMembershipRequired, d.DenyReason)
	assert.Equal(t, DecisionSourceTeamMember, d.Source)
}
