package receive

import (
	"github.com/thanos-io/thanos/pkg/store/storepb/prompb"
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
	TenantLabelPrefixes   map[string]string   `yaml:"tenantLabelPrefixes"`
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

// filterLabels drops tenant-label and external_labels when tenantExtract is on
func filterLabels(r *Writer, t prompb.TimeSeries, eset []string) labels.Labels {
	if !r.tenantExtract {
		return labelpb.ZLabelsToPromLabels(t.Labels)
	}

	lset := make(labels.Labels, 0, len(t.Labels))
	for _, l := range labelpb.ZLabelsToPromLabels(t.Labels) {
		if l.Name == r.tenantLabelName || sliceContains(eset, l.Name) {
			// drop tenant-label and external_labels from metrics labels
			continue
		}
		lset = append(lset, l)
	}
	return lset
}
