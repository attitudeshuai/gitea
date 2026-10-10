// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package access

import (
	"testing"

	perm_model "gitea.dev/models/perm"
	"gitea.dev/models/unit"

	"github.com/stretchr/testify/assert"
)

func TestPickModeSource(t *testing.T) {
	// higher mode wins regardless of source rank
	got := pickModeSource(
		modeSource{mode: perm_model.AccessModeRead, source: DecisionSourceSiteAdmin},
		modeSource{mode: perm_model.AccessModeWrite, source: DecisionSourceRepoUnitPublic},
	)
	assert.Equal(t, perm_model.AccessModeWrite, got.mode)
	assert.Equal(t, DecisionSourceRepoUnitPublic, got.source)

	// equal mode: rank decides, site_admin > owner > collaborator > team_unit_mode > team_member > repo_unit_public
	order := []DecisionSource{
		DecisionSourceRepoUnitPublic,
		DecisionSourceTeamMember,
		DecisionSourceTeamUnitMode,
		DecisionSourceCollaborator,
		DecisionSourceOwner,
		DecisionSourceSiteAdmin,
	}
	for i := 1; i < len(order); i++ {
		got = pickModeSource(
			modeSource{mode: perm_model.AccessModeRead, source: order[i-1]},
			modeSource{mode: perm_model.AccessModeRead, source: order[i]},
		)
		assert.Equal(t, order[i], got.source, "%s should beat %s", order[i], order[i-1])
	}
}

func TestPermTracerGrantUnit(t *testing.T) {
	tr := newPermTracer()
	tr.grantUnit(unit.TypeCode, perm_model.AccessModeNone, DecisionSourceRepoUnitPublic)
	tr.grantUnit(unit.TypeCode, perm_model.AccessModeRead, DecisionSourceTeamMember)
	assert.Equal(t, perm_model.AccessModeRead, tr.unitsGrant[unit.TypeCode].mode)
	assert.Equal(t, DecisionSourceTeamMember, tr.unitsGrant[unit.TypeCode].source)

	// equal mode keeps the higher-rank source: team_unit_mode beats team_member
	tr.grantUnit(unit.TypeCode, perm_model.AccessModeRead, DecisionSourceTeamUnitMode)
	assert.Equal(t, DecisionSourceTeamUnitMode, tr.unitsGrant[unit.TypeCode].source)
}
