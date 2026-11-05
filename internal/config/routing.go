package config

import (
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const DefaultUseOrLoseShortWindowCapPct = 95

type UseOrLoseConfig struct {
	ShortWindowCapPct float64 `yaml:"short-window-cap-pct,omitempty" json:"short-window-cap-pct,omitempty"`
}

func (c UseOrLoseConfig) EffectiveShortWindowCapPct() float64 {
	if c.ShortWindowCapPct > 1 && c.ShortWindowCapPct <= 100 {
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
		UseOrLose       struct {
			ShortWindowCapPct *float64 `yaml:"short-window-cap-pct"`
		} `yaml:"use-or-lose"`
	}
	if err := node.Decode(&explicit); err != nil {
		return err
	}
	if value := explicit.UseOrLose.ShortWindowCapPct; value != nil && !(*value > 1 && *value <= 100) {
		log.Warnf("routing.use-or-lose.short-window-cap-pct must be a percentage greater than 1 and at most 100; got %g, using %d", *value, DefaultUseOrLoseShortWindowCapPct)
		decoded.UseOrLose.ShortWindowCapPct = DefaultUseOrLoseShortWindowCapPct
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
