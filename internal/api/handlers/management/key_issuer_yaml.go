package management

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"gopkg.in/yaml.v3"
)

func issuedKeysYAML(raw []byte, added *yaml.Node, removed []access.KeyPolicy) ([]byte, error) {
	canonical, _, err := config.NormalizeConfigLayout(raw, true)
	if err != nil {
		return nil, errors.New("invalid configuration")
	}
	var expected yaml.Node
	if yaml.Unmarshal(canonical, &expected) != nil || len(expected.Content) == 0 {
		return nil, errors.New("invalid configuration")
	}
	keys := configV8Node(expected.Content[0], []string{"access", "api-keys"})
	if keys == nil {
		keys = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		parent := configV8Node(expected.Content[0], []string{"access"})
		if parent == nil {
			parent = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setYAMLMappingValue(expected.Content[0], "access", parent)
		}
		setYAMLMappingValue(parent, "api-keys", keys)
	}
	remove := map[[3]string]bool{}
	for _, policy := range removed {
		remove[[3]string{policy.Key, policy.Name, policy.IssuedBy}] = true
	}
	retained := make([]*yaml.Node, 0)
	for _, entry := range keys.Content {
		if !remove[issuedKeyIdentity(entry)] {
			retained = append(retained, entry)
		}
	}
	keys.Kind, keys.Tag, keys.Content = yaml.SequenceNode, "!!seq", retained
	if added != nil {
		keys.Content = append(keys.Content, added)
	}
	result := append([]byte(nil), raw...)
	for _, policy := range removed {
		result, err = spliceIssuedKey(result, nil, &policy)
		if err != nil {
			return nil, err
		}
	}
	if added != nil {
		result, err = spliceIssuedKey(result, added, nil)
		if err != nil {
			return nil, err
		}
	}
	actual, _, err := config.NormalizeConfigLayout(result, true)
	if err != nil {
		return nil, errors.New("invalid updated configuration")
	}
	var actualValue, expectedValue any
	if yaml.Unmarshal(actual, &actualValue) != nil || expected.Decode(&expectedValue) != nil || !reflect.DeepEqual(actualValue, expectedValue) {
		return nil, errors.New("updated configuration differs from key edit")
	}
	return result, nil
}

func yamlByteOffset(raw []byte, node *yaml.Node) int {
	offset := 0
	for line := 1; line < node.Line; line++ {
		end := bytes.IndexByte(raw[offset:], '\n')
		if end < 0 {
			return len(raw)
		}
		offset += end + 1
	}
	for column := 1; column < node.Column && offset < len(raw); column++ {
		_, size := utf8.DecodeRune(raw[offset:])
		offset += size
	}
	return offset
}

func issuedKeyBlockSpan(raw []byte, node *yaml.Node) (int, int, int) {
	start := yamlByteOffset(raw, node)
	for start > 0 && raw[start-1] != '\n' {
		start--
	}
	for start > 0 && !bytes.HasPrefix(bytes.TrimLeft(raw[start:], " "), []byte("-")) {
		start--
		for start > 0 && raw[start-1] != '\n' {
			start--
		}
	}
	indent := 0
	for start+indent < len(raw) && raw[start+indent] == ' ' {
		indent++
	}
	end, cursor := start, start
	for cursor < len(raw) {
		n := bytes.IndexByte(raw[cursor:], '\n')
		if n < 0 {
			n = len(raw) - cursor - 1
		}
		next := cursor + n + 1
		line := raw[cursor:next]
		trimmed := bytes.TrimSpace(line)
		depth := len(line) - len(bytes.TrimLeft(line, " "))
		if cursor > start && len(trimmed) > 0 && trimmed[0] != '#' && depth <= indent {
			break
		}
		if len(trimmed) > 0 && trimmed[0] != '#' {
			end = next
		}
		cursor = next
	}
	return start, end, indent
}

func spliceIssuedKey(raw []byte, added *yaml.Node, removed *access.KeyPolicy) ([]byte, error) {
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || len(doc.Content) == 0 {
		return nil, errors.New("invalid configuration")
	}
	keys := configV8Node(doc.Content[0], []string{"access", "api-keys"})
	if keys == nil {
		keys = configV8Node(doc.Content[0], []string{"api-keys"})
		if keys != nil && keys.Kind == yaml.MappingNode {
			keys = nil
		}
	}
	splice := func(start, end int, value []byte) []byte {
		out := append([]byte(nil), raw[:start]...)
		out = append(out, value...)
		return append(out, raw[end:]...)
	}
	if keys == nil {
		if added == nil {
			return raw, nil
		}
		sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{added}, Style: yaml.FlowStyle}
		encoded, err := yaml.Marshal(sequence)
		if err != nil {
			return nil, err
		}
		parent := configV8Node(doc.Content[0], []string{"access"})
		if parent == nil {
			return append(append(raw, []byte("\naccess:\n  api-keys: ")...), encoded...), nil
		}
		if parent.Kind != yaml.MappingNode {
			return nil, errors.New("invalid access mapping")
		}
		if parent.Style&yaml.FlowStyle != 0 {
			start := yamlByteOffset(raw, parent)
			end := issuedKeyCollectionEnd(raw, start)
			prefix := ""
			if len(parent.Content) > 0 && !issuedKeyTrailingComma(raw, parent) {
				prefix = ", "
			}
			return splice(end-1, end-1, []byte(prefix+"api-keys: "+strings.TrimSpace(string(encoded)))), nil
		}
		start := yamlByteOffset(raw, parent)
		for start > 0 && raw[start-1] != '\n' {
			start--
		}
		return splice(start, start, []byte(strings.Repeat(" ", parent.Column-1)+"api-keys: "+string(encoded))), nil
	}
	if keys.Kind != yaml.SequenceNode && keys.Tag != "!!null" {
		return nil, errors.New("invalid client key sequence")
	}
	if removed != nil {
		for i, entry := range keys.Content {
			if issuedKeyIdentity(entry) != [3]string{removed.Key, removed.Name, removed.IssuedBy} {
				continue
			}
			if keys.Style&yaml.FlowStyle != 0 {
				start := yamlByteOffset(raw, entry)
				end := clientKeyPolicyFlowEnd(raw, start)
				if i+1 < len(keys.Content) {
					end++
				} else if i > 0 {
					start = clientKeyPolicyFlowEnd(raw, yamlByteOffset(raw, keys.Content[i-1]))
				} else if end < len(raw) && raw[end] == ',' {
					end++
				}
				return splice(start, end, nil), nil
			}
			start, end, indent := issuedKeyBlockSpan(raw, entry)
			var replacement []byte
			if len(keys.Content) == 1 {
				replacement = []byte(strings.Repeat(" ", indent) + "[]\n")
			}
			return splice(start, end, replacement), nil
		}
		return raw, nil
	}
	if added == nil {
		return raw, nil
	}
	if keys.Style&yaml.FlowStyle != 0 || keys.Tag == "!!null" {
		copy := cloneConfigV8Node(added)
		copy.Style = yaml.FlowStyle
		encoded, err := yaml.Marshal(copy)
		if err != nil {
			return nil, err
		}
		encoded = bytes.TrimSpace(encoded)
		start := yamlByteOffset(raw, keys)
		if keys.Tag == "!!null" {
			end := start + len(keys.Value)
			return splice(start, end, append(append([]byte{'['}, encoded...), ']')), nil
		}
		end := issuedKeyCollectionEnd(raw, start)
		if len(keys.Content) > 0 && !issuedKeyTrailingComma(raw, keys) {
			encoded = append([]byte(", "), encoded...)
		}
		return splice(end-1, end-1, encoded), nil
	}
	if len(keys.Content) == 0 {
		return nil, errors.New("invalid empty sequence")
	}
	_, end, indent := issuedKeyBlockSpan(raw, keys.Content[len(keys.Content)-1])
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{added}}
	encoded, err := yaml.Marshal(sequence)
	if err != nil {
		return nil, err
	}
	padding := strings.Repeat(" ", indent)
	encoded = []byte(padding + strings.ReplaceAll(strings.TrimSuffix(string(encoded), "\n"), "\n", "\n"+padding) + "\n")
	if end > 0 && raw[end-1] != '\n' {
		encoded = append([]byte{'\n'}, encoded...)
	}
	return splice(end, end, encoded), nil
}

func issuedKeyCollectionEnd(raw []byte, start int) int {
	for cursor := start + 1; cursor < len(raw); {
		end := clientKeyPolicyFlowEnd(raw, cursor)
		if end >= len(raw) {
			return end
		}
		if raw[end] == ']' || raw[end] == '}' {
			return end + 1
		}
		cursor = end + 1
	}
	return len(raw)
}

func issuedKeyTrailingComma(raw []byte, node *yaml.Node) bool {
	end := clientKeyPolicyFlowEnd(raw, yamlByteOffset(raw, node.Content[len(node.Content)-1]))
	return end < len(raw) && raw[end] == ','
}

func issuedKeyIdentity(node *yaml.Node) [3]string {
	var identity [3]string
	for i, field := range []string{"key", "name", "issued-by"} {
		if value := configV8Node(node, []string{field}); value != nil {
			identity[i] = value.Value
		}
	}
	return identity
}
