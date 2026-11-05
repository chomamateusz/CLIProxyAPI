package config

import (
	"strings"

	"gopkg.in/yaml.v3"
)

const DefaultUseOrLoseShortWindowCapPct = 95

type UseOrLoseConfig struct {
	ShortWindowCapPct float64 `yaml:"short-window-cap-pct,omitempty" json:"short-window-cap-pct,omitempty"`
}

func (c UseOrLoseConfig) EffectiveShortWindowCapPct() float64 {
	if c.ShortWindowCapPct > 0 && c.ShortWindowCapPct <= 100 {
		return c.ShortWindowCapPct
	}
	return DefaultUseOrLoseShortWindowCapPct
}

func (c *RoutingConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain RoutingConfig
	decoded := plain(*c)
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	var explicit struct {
		SessionAffinity *bool `yaml:"session-affinity"`
	}
	if err := node.Decode(&explicit); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(decoded.Strategy), "use-or-lose") {
		if explicit.SessionAffinity == nil {
			decoded.SessionAffinity = true
		}
		decoded.UseOrLose.ShortWindowCapPct = decoded.UseOrLose.EffectiveShortWindowCapPct()
	}
	*c = RoutingConfig(decoded)
	return nil
}
