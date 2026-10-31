package isureal

import "testing"

// isureal-guard の parseFailure が失敗として数える条件と一致すること
func TestUsedByGuard(t *testing.T) {
	cases := []struct {
		action string
		status int
		abuse  string
		want   bool
	}{
		{"login", 401, "", true},
		{"login", 200, "", false},
		{"put_profile_image", 400, "invalid_image", true},
		{"put_profile_image", 400, "", false},
		{"put_profile_image", 204, "", false},
		{"get_image", 403, "invalid_signature", true},
		{"get_image", 403, "", false},
		{"create_post", 201, "", false},
		{"create_friend_request", 201, "", false},
	}
	for _, c := range cases {
		if got := usedByGuard(c.action, c.status, c.abuse); got != c.want {
			t.Errorf("usedByGuard(%q, %d, %q) = %v, want %v", c.action, c.status, c.abuse, got, c.want)
		}
	}
}
