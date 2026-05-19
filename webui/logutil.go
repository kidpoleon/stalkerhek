package webui

import (
	"fmt"
	"log"
	"time"
)

// LogInfo writes a tagged line to the standard logger and the /logs buffer.
func LogInfo(tag, format string, args ...any) {
	log.Printf("[%s] "+format, append([]any{tag}, args...)...)
}

// LogWarn writes a warning tagged line.
func LogWarn(tag, format string, args ...any) {
	log.Printf("[WARN %s] "+format, append([]any{tag}, args...)...)
}

// LogStream logs HLS/proxy stream events without flooding (one line per event).
func LogStream(profile, channel, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[STREAM profile=%s channel=%q] %s", profile, channel, msg)
}

// LogProfile is shorthand for profile lifecycle messages.
func LogProfile(profileID int, name, format string, args ...any) {
	log.Printf("[PROFILE id=%d name=%q] "+format, append([]any{profileID, name}, args...)...)
}

func logWithTS(tag, level, format string, args ...any) {
	line := fmt.Sprintf("[%s] [%s] %s", time.Now().Format("2006-01-02 15:04:05"), level+":"+tag, fmt.Sprintf(format, args...))
	appendInstanceLogLine(line)
}
