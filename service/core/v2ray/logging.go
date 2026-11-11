package v2ray

import (
	"strings"
	"time"

	"github.com/v2rayA/v2rayA/core/coreObj"
)

func (t *Template) setLog(level string) {
	// Core connection failures can be emitted at info level. Never discard
	// the error stream at the default info level, even for latency templates.
	t.Log = &coreObj.Log{Loglevel: "info"}
	switch level {
	case "warn":
		t.Log.Loglevel, t.Log.Access = "warning", "none"
	case "error":
		t.Log.Loglevel, t.Log.Access = "error", "none"
	}
}

func coreLogLine(line string) (level, message string) {
	fields := strings.SplitN(line, " ", 3)
	if len(fields) == 3 {
		if _, err := time.Parse("2006/01/02 15:04:05", fields[0]+" "+fields[1]); err == nil {
			line = fields[2]
		}
	}
	// Preserve core severity so warnings/errors survive V2RAYA_LOG_LEVEL.
	for _, severity := range []string{"error", "warning", "debug"} {
		if strings.HasPrefix(strings.ToLower(line), "["+severity+"]") {
			return severity, line
		}
	}
	return "info", line
}
