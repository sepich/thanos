package receive

import (
	"os"
	"sort"

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
func getExtLabels(all []labelpb.ZLabel, extLabels []string) (res labels.Labels) {
	lset := labelpb.ZLabelsToPromLabels(labelpb.DeepCopy(all))
	sort.Strings(extLabels)
	return lset.WithLabels(extLabels...)
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
