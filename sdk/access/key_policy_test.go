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
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.AllowsModel(tc.model); got != tc.want {
				t.Fatalf("AllowsModel() = %v, want %v", got, tc.want)
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.AllowsCredential(tc.file, tc.id, tc.label); got != tc.want {
				t.Fatalf("AllowsCredential() = %v, want %v", got, tc.want)
			}
		})
	}
}
