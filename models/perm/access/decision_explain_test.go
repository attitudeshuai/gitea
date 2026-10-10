// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package access

import (
	"testing"

	audit_model "gitea.dev/models/audit"
	"gitea.dev/models/db"
	deploykey_model "gitea.dev/models/deploykey"
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

type explainCase struct {
	name   string
	repoID int64
	user   *user_model.User
	unit   unit.Type
	mode   perm_model.AccessMode
	source DecisionSource
	reason DenyReason
}

func (tc explainCase) loadRepo(t *testing.T) *repo_model.Repository {
	return unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: tc.repoID})
}

func runExplainCases(t *testing.T, cases []explainCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := tc.loadRepo(t)

			d, err := ExplainDoerRepoPermission(t.Context(), repo, tc.user, tc.unit)
			require.NoError(t, err)
			assert.Equal(t, tc.reason, d.DenyReason)
			assert.Equal(t, tc.source, d.Source)
			assert.Equal(t, tc.mode, d.AccessMode)
			assert.Equal(t, d.DenyReason == "", d.Allowed)
			assert.Equal(t, tc.unit, d.Unit)

			// Allowed must match the real permission of the same moment
			realPerm, err := GetDoerRepoPermission(t.Context(), repo, tc.user)
			require.NoError(t, err)
			assert.Equal(t, realPerm.CanRead(tc.unit), d.Allowed)
		})
	}
}

func TestExplainAllowedSources(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})

	runExplainCases(t, []explainCase{
		{
			name:   "SiteAdmin",
			repoID: 3, // private org3 repo
			user:   user1,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeOwner,
			source: DecisionSourceSiteAdmin,
		},
		{
			name:   "Owner",
			repoID: 1,
			user:   user2,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeOwner,
			source: DecisionSourceOwner,
		},
		{
			name:   "TeamOwnerMember",
			repoID: 3, // user2 is in org3 owners team
			user:   user2,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeOwner,
			source: DecisionSourceTeamMember,
		},
		{
			name:   "TeamUnitMode",
			repoID: 3, // user4's team1 grants code via per-unit write row
			user:   user4,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeWrite,
			source: DecisionSourceTeamUnitMode,
		},
		{
			name:   "Collaborator",
			repoID: 4, // user4 is write collaborator
			user:   user4,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeWrite,
			source: DecisionSourceCollaborator,
		},
		{
			name:   "RepoUnitPublic",
			repoID: 1,
			user:   nil, // anonymous
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeRead,
			source: DecisionSourceRepoUnitPublic,
		},
	})
}

func TestExplainDenyReasons(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	user5 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})

	// add a projects unit to repo3, user5 reaches it only through a select-units team (authorize none)
	require.NoError(t, db.Insert(ctx, &repo_model.RepoUnit{
		RepoID: 3,
		Type:   unit.TypeProjects,
		Config: &repo_model.UnitConfig{},
	}))
	selectTeam := &organization.Team{OrgID: 3, Name: "unit-select", LowerName: "unit-select", AccessMode: perm_model.AccessModeNone}
	require.NoError(t, db.Insert(ctx, selectTeam))
	require.NoError(t, db.Insert(ctx,
		&organization.OrgUser{OrgID: 3, UID: user5.ID, IsPublic: true},
		&organization.TeamUser{OrgID: 3, TeamID: selectTeam.ID, UID: user5.ID},
		&organization.TeamRepo{OrgID: 3, TeamID: selectTeam.ID, RepoID: 3},
		&organization.TeamUnit{OrgID: 3, TeamID: selectTeam.ID, Type: unit.TypeCode, AccessMode: perm_model.AccessModeRead},
	))

	// one deploy key for repo1
	require.NoError(t, db.Insert(ctx, &deploykey_model.DeployKey{
		RepoID: 1,
		Name:   "test-key",
		Mode:   perm_model.AccessModeRead,
	}))
	keyUser := user_model.NewDeployKeyUserWithKeyID(1)

	runExplainCases(t, []explainCase{
		{
			name:   "UnauthenticatedPrivateRepo",
			repoID: 2, // private repo of public user2
			user:   nil,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeNone,
			source: DecisionSourceRepoUnitPublic,
			reason: DenyReasonUnauthenticated,
		},
		{
			name:   "OwnerInvisibleAnonymous",
			repoID: 38, // public repo on limited_org
			user:   nil,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeNone,
			source: DecisionSourceOwner,
			reason: DenyReasonOwnerInvisible,
		},
		{
			name:   "OwnerInvisibleSigned",
			repoID: 40, // public repo on privated_org, user2 is neither member nor collaborator
			user:   user2,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeNone,
			source: DecisionSourceOwner,
			reason: DenyReasonOwnerInvisible,
		},
		{
			name:   "OrgMembershipRequired",
			repoID: 39, // private repo on limited_org, user4 is a non-member signed user
			user:   user4,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeNone,
			source: DecisionSourceTeamMember,
			reason: DenyReasonOrgMembershipRequired,
		},
		{
			name:   "TeamUnitNotGranted",
			repoID: 3, // org3 member via select-units team, projects not granted
			user:   user5,
			unit:   unit.TypeProjects,
			mode:   perm_model.AccessModeNone,
			source: DecisionSourceTeamUnitMode,
			reason: DenyReasonTeamUnitNotGranted,
		},
		{
			name:   "RepoUnitRestricted",
			repoID: 2, // private individual repo, user4 not collaborator
			user:   user4,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeNone,
			source: DecisionSourceRepoUnitPublic,
			reason: DenyReasonRepoUnitRestricted,
		},
		{
			name:   "DeployKeyOtherRepository",
			repoID: 2, // key 1 belongs to repo1
			user:   keyUser,
			unit:   unit.TypeCode,
			mode:   perm_model.AccessModeNone,
			source: DecisionSourceCollaborator,
			reason: DenyReasonCredentialInsufficient,
		},
		{
			name:   "DeployKeyNonGitUnit",
			repoID: 1, // key reaches only code/wiki
			user:   keyUser,
			unit:   unit.TypeIssues,
			mode:   perm_model.AccessModeNone,
			source: DecisionSourceCollaborator,
			reason: DenyReasonCredentialInsufficient,
		},
	})
}

func TestExplainReadOnly(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 39})

	beforeEvents, err := db.Count[audit_model.Event](ctx, nil)
	require.NoError(t, err)
	beforeAccess, err := db.Count[Access](ctx, nil)
	require.NoError(t, err)

	cacheCtx := cache.WithCacheContext(ctx)
	d, err := ExplainDoerRepoPermission(cacheCtx, repo, user4, unit.TypeCode)
	require.NoError(t, err)
	assert.False(t, d.Allowed)

	afterEvents, err := db.Count[audit_model.Event](ctx, nil)
	require.NoError(t, err)
	afterAccess, err := db.Count[Access](ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, beforeEvents, afterEvents)
	assert.Equal(t, beforeAccess, afterAccess)

	// no permission cache writes either
	c := cache.GetContextCache(cacheCtx)
	require.NotNil(t, c)
	_, ok := c.Get(cachegroup.RepoUserPermission, RepoUserPermissionCacheKey(repo.ID, user4))
	assert.False(t, ok)
}
