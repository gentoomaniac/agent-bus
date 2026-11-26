package logging

import (
	zlog "github.com/rs/zerolog/log"
)

// Infof adapts the logger to the printf-style Log hooks of the components: it logs at info level,
// with prefix before the message.
func Infof(prefix string) func(format string, args ...any) {
	return func(format string, args ...any) { zlog.Info().Msgf(prefix+format, args...) }
}
