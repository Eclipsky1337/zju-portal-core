package ping

import (
	"fmt"
	"time"
)

func timeIt(f func() interface{}) (int64, interface{}) {
	startAt := time.Now()
	result := f()
	return time.Since(startAt).Nanoseconds(), result
}

// Target is a ping
type Target struct {
	Host string
	Port int

	Counter  int
	Interval time.Duration
	Timeout  time.Duration
}

func (target Target) String() string {
	return fmt.Sprintf("tcp://%s:%d", target.Host, target.Port)
}

// Result ...
type Result struct {
	Counter        int
	SuccessCounter int
	Target         *Target

	MinDuration   time.Duration
	MaxDuration   time.Duration
	TotalDuration time.Duration
}

// Avg return the average time of ping
func (result Result) Avg() time.Duration {
	if result.SuccessCounter == 0 {
		return 0
	}
	return result.TotalDuration / time.Duration(result.SuccessCounter)
}
