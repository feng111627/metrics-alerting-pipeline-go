package metrics

import "time"

func nowWallMs() int64 { return time.Now().UnixNano() / int64(time.Millisecond) }
