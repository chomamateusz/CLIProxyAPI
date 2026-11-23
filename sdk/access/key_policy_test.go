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
		{"encoded name does not match exact allow", "claude-fable-5-dd-5.5-tpg", "gpt-5.5", KeyPolicy{Models: []string{"claude-fable-5-dd-5.5-tpg"}}, false},
		{"decoded name matches exact requested allow", "claude-fable-5-dd-los-2.6-tpg(high)", "backend-model", KeyPolicy{Models: []string{"gpt-6.2-sol(high)"}}, true},
		{"decoded name matches resolved wildcard allow", "client-model", "claude-fable-5-dd-los-2.6-tpg(high)", KeyPolicy{Models: []string{"gpt-6.2-*"}}, true},
		{"decoded name matches exact resolved denial", "client-model", "claude-fable-5-dd-los-2.6-tpg(high)", KeyPolicy{ExcludedModels: []string{"gpt-6.2-sol(high)"}}, false},
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

func TestKeyPolicyMatchesCanonicalModelIDs(t *testing.T) {
	policy := KeyPolicy{ExcludedModels: []string{"claude-fable-*", "gpt-6.2-astra"}}
	for _, tc := range []struct {
		model, resolved string
		want            bool
	}{
		{"claude-fable-5-dd-los-2.6-tpg", "gpt-6.2-sol", true},
		{"claude-fable-5-dd-artsa-2.6-tpg(high)", "gpt-6.2-astra(high)", false},
		{"claude-fable-5-dd-2-5-elbaf-edualc", "claude-fable-5-2", false},
		{"claude-fable-5-2", "claude-fable-5-2", false},
		{"claude-opus-5-5", "claude-opus-5-5", true},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := policy.AllowsModel(tc.model); got != tc.want {
				t.Errorf("AllowsModel() = %v, want %v", got, tc.want)
			}
			if got := policy.AllowsModelRoute(tc.model, tc.resolved); got != tc.want {
				t.Errorf("AllowsModelRoute() = %v, want %v", got, tc.want)
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
