// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package access

import (
	"slices"

	perm_model "gitea.dev/models/perm"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
)

// DecisionSource identifies the permission layer that effectively decides a decision.
type DecisionSource string

const (
	// DecisionSourceOwner is the repository owner's own grant.
	DecisionSourceOwner DecisionSource = "owner"
	// DecisionSourceCollaborator is an explicit per-repository collaborator grant.
	DecisionSourceCollaborator DecisionSource = "collaborator"
	// DecisionSourceTeamMember is a team membership-level (authorize) grant.
	DecisionSourceTeamMember DecisionSource = "team_member"
	// DecisionSourceTeamUnitMode is a team's per-unit configured mode.
	DecisionSourceTeamUnitMode DecisionSource = "team_unit_mode"
	// DecisionSourceRepoUnitPublic is a repository unit public (anonymous/everyone) mode.
	DecisionSourceRepoUnitPublic DecisionSource = "repo_unit_public"
	// DecisionSourceSiteAdmin is the site administrator override.
	DecisionSourceSiteAdmin DecisionSource = "site_admin"
)

// DenyReason is the stable code explaining why a decision is denied.
type DenyReason string

const (
	DenyReasonUnauthenticated        DenyReason = "unauthenticated"
	DenyReasonOwnerInvisible         DenyReason = "owner_invisible"
	DenyReasonOrgMembershipRequired  DenyReason = "org_membership_required"
	DenyReasonTeamUnitNotGranted     DenyReason = "team_unit_not_granted"
	DenyReasonRepoUnitRestricted     DenyReason = "repo_unit_restricted"
	DenyReasonCredentialInsufficient DenyReason = "credential_insufficient"
)

// Decision is the explained permission result for one repository unit.
type Decision struct {
	Allowed    bool
	AccessMode perm_model.AccessMode
	Source     DecisionSource
	DenyReason DenyReason
	Unit       unit.Type
}

type modeSource struct {
	mode   perm_model.AccessMode
	source DecisionSource
}

// sourceRank breaks ties when two layers provide the same mode; the more specific layer wins.
var sourceRank = map[DecisionSource]int{
	DecisionSourceSiteAdmin:      6,
	DecisionSourceOwner:          5,
	DecisionSourceCollaborator:   4,
	DecisionSourceTeamUnitMode:   3,
	DecisionSourceTeamMember:     2,
	DecisionSourceRepoUnitPublic: 1,
}

func pickModeSource(cur, cand modeSource) modeSource {
	if cand.mode > cur.mode || (cand.mode == cur.mode && sourceRank[cand.source] > sourceRank[cur.source]) {
		return cand
	}
	return cur
}

// permTracer records pipeline branch outcomes while the real permission is computed.
// It exists only for explain calls; a nil tracer keeps the normal path unchanged.
type permTracer struct {
	collaborator bool
	isOrg        bool

	terminal  *Decision // pipeline branches that return before unit-level analysis
	ungranted *Decision // denial for a unit without a read grant; nil means classify by membership

	unitsMode  bool                     // whether Permission.unitsMode is a present map
	unitsGrant map[unit.Type]modeSource // best explicit grant per unit (collaborator/min-public/team)

	base modeSource // provenance of the global AccessMode
}

func newPermTracer() *permTracer {
	return &permTracer{}
}

func (tr *permTracer) setCollaborator(v bool) {
	tr.collaborator = v
}

func (tr *permTracer) finishAllowed(mode perm_model.AccessMode, source DecisionSource) {
	tr.terminal = &Decision{Allowed: true, AccessMode: mode, Source: source}
}

func (tr *permTracer) finishDenied(reason DenyReason, source DecisionSource) {
	tr.terminal = &Decision{AccessMode: perm_model.AccessModeNone, DenyReason: reason, Source: source}
}

func (tr *permTracer) setBase(mode perm_model.AccessMode, source DecisionSource) {
	tr.base = modeSource{mode: mode, source: source}
}

func (tr *permTracer) initUnitsMode() {
	tr.unitsMode = true
	if tr.unitsGrant == nil {
		tr.unitsGrant = map[unit.Type]modeSource{}
	}
}

func (tr *permTracer) grantUnit(tp unit.Type, mode perm_model.AccessMode, source DecisionSource) {
	if tr.unitsGrant == nil {
		tr.unitsGrant = map[unit.Type]modeSource{}
	}
	ms := modeSource{mode: mode, source: source}
	if cur, ok := tr.unitsGrant[tp]; ok {
		ms = pickModeSource(cur, ms)
	}
	tr.unitsGrant[tp] = ms
}

// build derives the Decision for unitType from the fully computed Permission,
// mirroring Permission.UnitAccessMode exactly. repoUnits is the raw unit list
// before finalProcessRepoUnitPermission filtered it.
func (tr *permTracer) build(unitType unit.Type, p *Permission, repoUnits []*repo_model.RepoUnit) *Decision {
	if tr.terminal != nil {
		d := *tr.terminal
		d.Unit = unitType
		if d.Allowed && !slices.ContainsFunc(repoUnits, func(u *repo_model.RepoUnit) bool { return u.Type == unitType }) {
			// admin/owner/public grants still do not reach a unit the repository does not have
			d.Allowed = false
			d.AccessMode = perm_model.AccessModeNone
			d.DenyReason = DenyReasonRepoUnitRestricted
			d.Source = DecisionSourceRepoUnitPublic
		}
		return &d
	}

	hasUnit := slices.ContainsFunc(repoUnits, func(u *repo_model.RepoUnit) bool { return u.Type == unitType })

	var best modeSource
	addPublic := func() {
		publicMode := max(p.anonymousAccessMode[unitType], p.everyoneAccessMode[unitType])
		if publicMode > perm_model.AccessModeNone {
			best = pickModeSource(best, modeSource{mode: publicMode, source: DecisionSourceRepoUnitPublic})
		}
	}

	if tr.unitsMode {
		if g, ok := tr.unitsGrant[unitType]; ok {
			// explicit branch: admin/owner AccessMode overrides, public maps are ignored
			best = g
			if tr.base.mode >= perm_model.AccessModeAdmin {
				best = pickModeSource(best, tr.base)
			}
		} else {
			best = tr.base
			addPublic()
		}
	} else {
		best = tr.base
		addPublic()
	}

	if !hasUnit {
		best = modeSource{}
	}

	d := &Decision{AccessMode: best.mode, Source: best.source, Unit: unitType}
	if best.mode >= perm_model.AccessModeRead {
		d.Allowed = true
		return d
	}

	if tr.ungranted != nil {
		d.DenyReason = tr.ungranted.DenyReason
		d.Source = tr.ungranted.Source
		return d
	}

	d.DenyReason, d.Source = tr.classifyDeny(hasUnit)
	return d
}

func (tr *permTracer) classifyDeny(hasUnit bool) (DenyReason, DecisionSource) {
	if !hasUnit {
		return DenyReasonRepoUnitRestricted, DecisionSourceRepoUnitPublic
	}
	if tr.isOrg && !tr.collaborator {
		if tr.base.source == DecisionSourceTeamMember {
			return DenyReasonTeamUnitNotGranted, DecisionSourceTeamUnitMode
		}
		return DenyReasonOrgMembershipRequired, DecisionSourceTeamMember
	}
	return DenyReasonRepoUnitRestricted, DecisionSourceRepoUnitPublic
}
