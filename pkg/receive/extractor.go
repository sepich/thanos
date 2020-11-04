package receive

import (
	"os"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/prometheus/pkg/labels"

	"gopkg.in/yaml.v2"

	"github.com/thanos-io/thanos/pkg/store/labelpb"
)

// ExtractorConfig represents the configuration for external_labels extraction from received metrics
type ExtractorConfig struct {
	DefaultExternalLabels []string            `yaml:"defaultExternalLabels"`
	TenantExternalLabels  map[string][]string `yaml:"tenantExternalLabels"`
}

// ParseExtractorConfig parses the raw configuration content and returns a ExtractorConfig.
func ParseExtractorConfig(content []byte, logger log.Logger) ExtractorConfig {
	var config ExtractorConfig
	if err := yaml.UnmarshalStrict(content, &config); err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}
	return config
}

// getExtLabels returns subset of external_labels from all labels
func getExtLabels(all []labelpb.Label, extLabels []string) (res labels.Labels) {
	for _, l := range all {
		// https://www.darkcoding.net/software/go-slice-search-vs-map-lookup/ slice lookup is faster than map for len()<5
		for _, e := range extLabels {
			if l.Name == e {
				res = append(res, labels.Label(l))
			}
		}
	}
	return res
}

// sliceContains returns true if strings contain the string
func sliceContains(a []string, x string) bool {
	for _, s := range a {
		if s == x {
			return true
		}
	}
	return false
}
