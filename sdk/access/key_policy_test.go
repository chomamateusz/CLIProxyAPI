package access

import "testing"

func TestKeyPolicyAllowsModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy KeyPolicy
		model  string
		want   bool
	}{
		{"empty", KeyPolicy{}, "anything", true},
		{"allow wildcard", KeyPolicy{Models: []string{"claude-*", "gpt-5*"}}, "gpt-5.4", true},
		{"allow miss", KeyPolicy{Models: []string{"claude-*"}}, "gpt-5", false},
		{"deny wildcard", KeyPolicy{ExcludedModels: []string{"claude-fable-*"}}, "claude-fable-5", false},
		{"deny wins", KeyPolicy{Models: []string{"claude-*"}, ExcludedModels: []string{"*-fable-*"}}, "claude-fable-5", false},
		{"case insensitive", KeyPolicy{Models: []string{"CLAUDE-*"}}, "Claude-Opus", true},
		{"deny case insensitive", KeyPolicy{ExcludedModels: []string{"CLAUDE-*"}}, "Claude-Opus", false},
		{"middle wildcard", KeyPolicy{Models: []string{"cl*de-*us"}}, "claude-opus", true},
		{"empty wildcard", KeyPolicy{Models: []string{"gpt*5"}}, "gpt5", true},
		{"literal question mark", KeyPolicy{Models: []string{"gpt-?"}}, "gpt-5", false},
		{"literal brackets", KeyPolicy{Models: []string{"[ab]"}}, "a", false},
		{"suffix exact allow", KeyPolicy{Models: []string{"gpt-5"}}, "gpt-5(high)", true},
		{"suffix exact deny", KeyPolicy{ExcludedModels: []string{"gpt-5"}}, "gpt-5(high)", false},
		{"suffixed exact allow", KeyPolicy{Models: []string{"gpt-5(high)"}}, "gpt-5(high)", true},
		{"suffixed exact deny", KeyPolicy{ExcludedModels: []string{"gpt-5(high)"}}, "gpt-5(high)", false},
		{"suffixed wildcard deny", KeyPolicy{ExcludedModels: []string{"*(high)"}}, "gpt-5(high)", false},
		{"suffixed wildcard permits plain", KeyPolicy{ExcludedModels: []string{"*(high)"}}, "gpt-5", true},
		{"unrelated namespace does not widen allow", KeyPolicy{Models: []string{"gpt-5"}}, "custom/gpt-5", false},
		{"unrelated namespace does not match exclusion", KeyPolicy{ExcludedModels: []string{"gpt-5"}}, "custom/gpt-5", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.AllowsModel(tc.model); got != tc.want {
				t.Fatalf("AllowsModel() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKeyPolicyAllowsModelRoute(t *testing.T) {
	for _, tc := range []struct {
		name, requested, resolved string
		policy                    KeyPolicy
		want                      bool
	}{
		{"wildcard alias cannot widen", "claude-fable-5-dd-5.5-tpg", "gpt-5.5", KeyPolicy{Models: []string{"claude-*"}}, false},
		{"exact alias can select resolved", "claude-fable-5-dd-5.5-tpg", "gpt-5.5", KeyPolicy{Models: []string{"claude-fable-5-dd-5.5-tpg"}}, true},
		{"resolved exclusion wins", "claude-fable-5-dd-supo-edualc", "claude-opus", KeyPolicy{ExcludedModels: []string{"claude-opus*"}}, false},
		{"credential prefix stripped by resolution", "team/gpt-5(high)", "gpt-5(high)", KeyPolicy{Models: []string{"gpt-5"}}, true},
		{"namespace remains distinct", "custom/gpt-5", "custom/gpt-5", KeyPolicy{Models: []string{"gpt-5"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.AllowsModelRoute(tc.requested, tc.resolved); got != tc.want {
				t.Fatalf("AllowsModelRoute() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKeyPolicyAllowsCredential(t *testing.T) {
	for _, tc := range []struct {
		name            string
		policy          KeyPolicy
		file, id, label string
		want            bool
	}{
		{"empty", KeyPolicy{}, "", "api-id", "", true},
		{"basename", KeyPolicy{Credentials: []string{"claude-work.json"}}, "/auths/claude-work.json", "id", "", true},
		{"without extension", KeyPolicy{Credentials: []string{"claude-work"}}, "/auths/claude-work.JSON", "id", "", true},
		{"id", KeyPolicy{Credentials: []string{"credential-id"}}, "/auths/other.json", "credential-id", "", true},
		{"wildcard case", KeyPolicy{Credentials: []string{"CLAUDE-*.JSON"}}, "/auths/claude-work.json", "id", "", true},
		{"deny wins", KeyPolicy{Credentials: []string{"claude-*"}, ExcludedCredentials: []string{"*work*"}}, "/auths/claude-work.json", "id", "", false},
		{"deny other identity", KeyPolicy{Credentials: []string{"id"}, ExcludedCredentials: []string{"claude-work"}}, "/auths/claude-work.json", "id", "", false},
		{"synthesized id", KeyPolicy{Credentials: []string{"api-*"}}, "", "api-id", "label", true},
		{"synthesized label", KeyPolicy{Credentials: []string{"work"}}, "", "api-id", "WORK", true},
		{"file ignores label", KeyPolicy{Credentials: []string{"work"}}, "other.json", "id", "work", false},
		{"path not matched", KeyPolicy{Credentials: []string{"/auths/*"}}, "/auths/work.json", "id", "", false},
		{"home id stem denied", KeyPolicy{ExcludedCredentials: []string{"work"}}, "", "work.json", "unrelated", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.AllowsCredential(tc.file, tc.id, tc.label); got != tc.want {
				t.Fatalf("AllowsCredential() = %v, want %v", got, tc.want)
			}
		})
	}
}
