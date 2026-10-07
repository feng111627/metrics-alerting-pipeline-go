package alerting

import "fmt"

var (
	ErrRuleNotFound  = ruleErr("alert rule not found")
	ErrDuplicateRule = ruleErr("alert rule already registered")
	ErrInvalidRule   = ruleErr("invalid alert rule")
)

type ruleErr string

func (e ruleErr) Error() string { return string(e) }

func invalidRule(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRule, fmt.Sprintf(format, args...))
}
