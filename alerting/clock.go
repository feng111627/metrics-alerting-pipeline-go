package alerting

import "time"

// RealClock 使用系统时间。
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// FakeClock 是测试用的手动时钟。
type FakeClock struct {
	now time.Time
}

func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t} }

func (c *FakeClock) Now() time.Time          { return c.now }
func (c *FakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }
