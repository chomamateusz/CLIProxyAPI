package management

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func clientKeyPolicyYAML(raw, canonical []byte, index int, updated *yaml.Node) ([]byte, bool, error) {
	var original yaml.Node
	if err := yaml.Unmarshal(raw, &original); err != nil || len(original.Content) == 0 {
		return nil, false, errors.New("invalid configuration")
	}
	keys := configV8Node(original.Content[0], []string{"access", "api-keys"})
	if keys == nil {
		return canonical, false, nil
	}
	if keys.Kind != yaml.SequenceNode || index >= len(keys.Content) {
		return nil, false, errors.New("invalid client key sequence")
	}
	node := keys.Content[index]
	replacement := cloneConfigV8Node(updated)
	replacement.HeadComment, replacement.FootComment = "", ""
	lines := bytes.SplitAfter(raw, []byte("\n"))
	startLine := node.Line - 1
	if startLine < 0 || startLine >= len(lines) {
		return nil, false, errors.New("invalid client key position")
	}
	var start, end int
	var encoded []byte
	var err error
	if keys.Style&yaml.FlowStyle != 0 {
		for _, line := range lines[:startLine] {
			start += len(line)
		}
		column := 1
		for column < node.Column && start < len(raw) {
			_, size := utf8.DecodeRune(raw[start:])
			start += size
			column++
		}
		end = clientKeyPolicyFlowEnd(raw, start)
		replacement.Style = yaml.FlowStyle
		encoded, err = yaml.Marshal(replacement)
		encoded = bytes.TrimSpace(encoded)
	} else {
		for startLine >= 0 && !bytes.HasPrefix(bytes.TrimLeft(lines[startLine], " "), []byte("-")) {
			startLine--
		}
		if startLine < 0 {
			return nil, false, errors.New("invalid client key position")
		}
		indent := len(lines[startLine]) - len(bytes.TrimLeft(lines[startLine], " "))
		endLine := startLine + 1
		for endLine < len(lines) {
			line := lines[endLine]
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 && trimmed[0] != '#' && len(line)-len(bytes.TrimLeft(line, " ")) <= indent {
				break
			}
			endLine++
		}
		for endLine > startLine+1 {
			trimmed := bytes.TrimSpace(lines[endLine-1])
			if len(trimmed) > 0 && trimmed[0] != '#' {
				break
			}
			endLine--
		}
		for _, line := range lines[:startLine] {
			start += len(line)
		}
		end = start
		for _, line := range lines[startLine:endLine] {
			end += len(line)
		}
		sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{replacement}}
		encoded, err = yaml.Marshal(sequence)
		if err == nil {
			encoded = bytes.ReplaceAll(encoded, []byte("\n"), []byte("\n"+strings.Repeat(" ", indent)))
			encoded = append([]byte(strings.Repeat(" ", indent)), bytes.TrimRight(encoded, " ")...)
		}
	}
	if err != nil || end <= start {
		return nil, false, errors.New("could not encode client key policy")
	}
	candidate := make([]byte, 0, len(raw)+len(encoded))
	candidate = append(candidate, raw[:start]...)
	candidate = append(candidate, encoded...)
	candidate = append(candidate, raw[end:]...)
	actual, _, err := config.NormalizeConfigLayout(candidate, true)
	if err != nil {
		return nil, false, errors.New("invalid updated configuration")
	}
	var actualValue, expectedValue any
	if yaml.Unmarshal(actual, &actualValue) != nil || yaml.Unmarshal(canonical, &expectedValue) != nil || !reflect.DeepEqual(actualValue, expectedValue) {
		return nil, false, errors.New("updated configuration differs from policy edit")
	}
	return candidate, true, nil
}

func clientKeyPolicyFlowEnd(raw []byte, start int) int {
	depth := 0
	var quote byte
	plain := false
	for i := start; i < len(raw); i++ {
		value := raw[i]
		if quote != 0 {
			if quote == '"' && value == '\\' {
				i++
			} else if value == quote {
				if quote == '\'' && i+1 < len(raw) && raw[i+1] == '\'' {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		switch value {
		case '\'', '"':
			if !plain {
				quote = value
			}
			plain = true
		case '#':
			if i == start || bytes.ContainsRune([]byte(" \t\r\n"), rune(raw[i-1])) {
				for i < len(raw) && raw[i] != '\n' {
					i++
				}
			}
		case '[', '{':
			depth++
			plain = false
		case ']', '}':
			if depth == 0 {
				return i
			}
			depth--
			plain = false
		case ',':
			if depth == 0 {
				return i
			}
			plain = false
		case ':':
			if i+1 < len(raw) && bytes.ContainsRune([]byte(" \t\r\n[{"), rune(raw[i+1])) {
				plain = false
			}
		case ' ', '\t', '\r', '\n':
		default:
			plain = true
		}
	}
	return len(raw)
}
