package metrics

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	labelNameRE  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
)

const (
	maxLabelNameLen    = 64
	maxLabelValueLen   = 256
	maxLabelsPerSeries = 16
)

// Labels 是一组键值对，键唯一。
type Labels map[string]string

// ValidateMetricName 校验指标名。
func ValidateMetricName(name string) error {
	if name == "" || len(name) > 128 || !metricNameRE.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}

// ValidateLabels 校验标签键与值的合法性。
func ValidateLabels(l Labels) error {
	if len(l) > maxLabelsPerSeries {
		return fmt.Errorf("%w: too many labels (%d > %d)", ErrInvalidLabel, len(l), maxLabelsPerSeries)
	}
	for k, v := range l {
		if !labelNameRE.MatchString(k) || len(k) > maxLabelNameLen {
			return fmt.Errorf("%w: bad label name %q", ErrInvalidLabel, k)
		}
		if len(v) > maxLabelValueLen {
			return fmt.Errorf("%w: label value too long for %q", ErrInvalidLabel, k)
		}
	}
	return nil
}

// Key 返回标签组合的规范编码，用于去重与索引。
// 形如 k1=v1\xffk2=v2，键按字典序排列，保证唯一且确定。
func (l Labels) Key() string {
	if len(l) == 0 {
		return ""
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(0xff)
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(l[k])
	}
	return b.String()
}

// Clone 返回标签的拷贝。
func (l Labels) Clone() Labels {
	c := make(Labels, len(l))
	for k, v := range l {
		c[k] = v
	}
	return c
}
