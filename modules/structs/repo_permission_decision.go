// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package structs

// RepoPermissionDecision is the read-only explained permission for one repository unit.
// swagger:model RepoPermissionDecision
type RepoPermissionDecision struct {
	// Allowed reports whether the subject can read the unit.
	Allowed bool `json:"allowed"`
	// Mode is the effective access mode.
	Mode AccessLevelName `json:"mode"`
	// Source is the permission layer that effectively decides.
	Source string `json:"source"`
	// DenyReason is the stable code when the decision is denied.
	DenyReason string `json:"deny_reason,omitempty"`
	// Unit is the queried unit machine name.
	Unit string `json:"unit"`
}

// PermissionDenyReasonCount is the count of denial events of one reason.
// swagger:model PermissionDenyReasonCount
type PermissionDenyReasonCount struct {
	// Reason is the stable deny reason code.
	Reason string `json:"reason"`
	// Count is the number of recorded events.
	Count int64 `json:"count"`
}
