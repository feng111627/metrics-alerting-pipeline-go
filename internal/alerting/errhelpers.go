package alerting

import "fmt"

func duplicateRuleError(name string) error {
	return fmt.Errorf("%w: %q", ErrDuplicateRule, name)
}

func ruleNotFoundError(name string) error {
	return fmt.Errorf("%w: %q", ErrRuleNotFound, name)
}
