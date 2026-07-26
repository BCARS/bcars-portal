package authz

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roleCapabilities mirrors the seed data from 0004_seed_roles.sql.
var roleCapabilities = map[string][]string{
	"administrator": func() []string {
		codes := make([]string, 0, len(All))
		for _, c := range All {
			codes = append(codes, c.Code)
		}
		return codes
	}(),
	"webmaster": {
		"session.self.read", "system.admin", "integration.config.write",
		"audit.read", "user.invite", "role.grant",
	},
	"president": {
		"session.self.read", "member.read", "member.create", "member.update",
		"member.deactivate", "member.export", "contact_method.write",
		"sharing_pref.write.officer", "membership.approve", "membership.lifecycle",
		"fcc.verify", "honorary.grant", "notes.write.officer",
		"import.upload", "import.commit", "audit.read",
	},
	"treasurer": {
		"session.self.read", "member.read", "member.create", "member.update",
		"member.deactivate", "member.export", "contact_method.write",
		"sharing_pref.write.officer", "membership.approve", "membership.lifecycle",
		"fcc.verify", "honorary.grant", "notes.write.officer",
		"import.upload", "import.commit", "audit.read",
		"notes.write.treasurer", "notes.read.treasurer",
	},
	"trustee": {
		"session.self.read", "member.read", "contact_method.write", "notes.write.officer",
	},
	"activities_manager": {
		"session.self.read", "member.read", "contact_method.write", "notes.write.officer",
	},
	"acs_coordinator": {
		"session.self.read", "member.read",
	},
	"member": {
		"session.self.read",
	},
}

func capsSet(codes []string) map[string]struct{} {
	m := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	return m
}

// TestAuthzMatrix verifies every (role, capability) pair: roles that should
// have a capability are allowed; roles that shouldn't are denied.
func TestAuthzMatrix(t *testing.T) {
	ctx := context.Background()

	for _, cap := range All {
		t.Run(cap.Code, func(t *testing.T) {
			for role, roleCaps := range roleCapabilities {
				principal := &Principal{
					UserID:       1,
					Capabilities: capsSet(roleCaps),
				}

				err := Authorize(ctx, principal, cap.Code, nil)

				shouldHave := false
				for _, rc := range roleCaps {
					if rc == cap.Code {
						shouldHave = true
						break
					}
				}

				if shouldHave {
					assert.NoError(t, err, "role %s should have capability %s", role, cap.Code)
				} else {
					assert.ErrorIs(t, err, ErrDenied, "role %s should NOT have capability %s", role, cap.Code)
				}
			}
		})
	}
}

func TestNilPrincipalDenied(t *testing.T) {
	err := Authorize(context.Background(), nil, "member.read", nil)
	assert.ErrorIs(t, err, ErrUnauthenticated)
}

func TestUnknownActionDenied(t *testing.T) {
	p := &Principal{UserID: 1, Capabilities: capsSet([]string{"member.read"})}
	err := Authorize(context.Background(), p, "nonexistent.capability", nil)
	assert.ErrorIs(t, err, ErrDenied)
}

func TestPublicEndpoint(t *testing.T) {
	require.NoError(t, AuthorizePublic())
}

func TestAdministratorHasAllCapabilities(t *testing.T) {
	adminCaps := capsSet(roleCapabilities["administrator"])
	p := &Principal{UserID: 1, Capabilities: adminCaps}

	for _, cap := range All {
		err := Authorize(context.Background(), p, cap.Code, nil)
		assert.NoError(t, err, "administrator should have %s", cap.Code)
	}
}

func TestMemberCanOnlyReadSelf(t *testing.T) {
	memberCaps := capsSet(roleCapabilities["member"])
	p := &Principal{UserID: 1, Capabilities: memberCaps}

	assert.NoError(t, Authorize(context.Background(), p, "session.self.read", nil))
	assert.ErrorIs(t, Authorize(context.Background(), p, "member.read", nil), ErrDenied)
	assert.ErrorIs(t, Authorize(context.Background(), p, "member.create", nil), ErrDenied)
}

// TestTreasurerSupersetOfPresident verifies treasurer has everything president
// has plus treasurer-specific notes capabilities.
func TestTreasurerSupersetOfPresident(t *testing.T) {
	presidentCaps := roleCapabilities["president"]
	treasurerSet := capsSet(roleCapabilities["treasurer"])

	for _, cap := range presidentCaps {
		_, ok := treasurerSet[cap]
		assert.True(t, ok, "treasurer should include president capability: %s", cap)
	}

	// Treasurer extras.
	_, ok := treasurerSet["notes.write.treasurer"]
	assert.True(t, ok)
	_, ok = treasurerSet["notes.read.treasurer"]
	assert.True(t, ok)
}
