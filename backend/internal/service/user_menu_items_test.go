package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserMenuItemsIncludeHelpCenter(t *testing.T) {
	items := ParseUserMenuItems("")
	require.Contains(t, items, "help_center")

	normalized := ParseUserMenuItems(`["keys","help-center","support"]`)
	require.Equal(t, []string{"api_keys", "help_center", "support_contact"}, normalized)
}

func TestNormalizeUserMenuPermissionsIncludesSecondLevelAgency(t *testing.T) {
	permissions := NormalizeUserMenuPermissions([]string{"second_level_agency", "admin_users"})
	require.Equal(t, []string{"second_level_agency"}, permissions)
}
