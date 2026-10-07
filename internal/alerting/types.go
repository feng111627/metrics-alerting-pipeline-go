package alerting

// AlertState is one node of the alert rule state machine.
type AlertState uint8

const (
	// StateNoData: no fresh sample exists in the evaluation window.
	StateNoData AlertState = iota
	// StatePending: condition matches but the for-duration has not elapsed.
	StatePending
	// StateFiring: condition has held continuously for the for-duration.
	StateFiring
	// StateRecovered: transient state reported once after a firing alert
	// sees the condition clear (or data disappear); the next evaluation
	// settles back into StateNoData/StatePending.
	StateRecovered
)

func (s AlertState) String() string {
	switch s {
	case StateNoData:
		return "nodata"
	case StatePending:
		return "pending"
	case StateFiring:
		return "firing"
	case StateRecovered:
		return "recovered"
	default:
		return "unknown"
	}
}

// Operator is the comparison used by a Rule.
type Operator uint8

const (
	OpGt Operator = iota + 1
	OpGe
	OpLt
	OpLe
	OpEq
	OpNe
)

func (op Operator) String() string {
	switch op {
	case OpGt:
		return ">"
	case OpGe:
		return ">="
	case OpLt:
		return "<"
	case OpLe:
		return "<="
	case OpEq:
		return "=="
	case OpNe:
		return "!="
	default:
		return "?"
	}
}

func (op Operator) match(v, threshold float64) bool {
	switch op {
	case OpGt:
		return v > threshold
	case OpGe:
		return v >= threshold
	case OpLt:
		return v < threshold
	case OpLe:
		return v <= threshold
	case OpEq:
		return v == threshold
	case OpNe:
		return v != threshold
	default:
		return false
	}
}
