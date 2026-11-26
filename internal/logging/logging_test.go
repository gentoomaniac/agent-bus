package logging

import (
	"testing"

	"github.com/rs/zerolog"
)

func TestSetupMapsTheFlagsToALevel(t *testing.T) {
	original := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(original) })

	tests := []struct {
		name   string
		config LoggingConfig
		want   zerolog.Level
	}{
		{"errors by default", LoggingConfig{Json: true}, zerolog.ErrorLevel},
		{"-v adds warnings", LoggingConfig{Json: true, Verbosity: 1}, zerolog.WarnLevel},
		{"-vv adds info", LoggingConfig{Json: true, Verbosity: 2}, zerolog.InfoLevel},
		{"-vvv adds debug", LoggingConfig{Json: true, Verbosity: 3}, zerolog.DebugLevel},
		{"--debug is debug", LoggingConfig{Json: true, Debug: true}, zerolog.DebugLevel},
		{"-q logs nothing, whatever -v says", LoggingConfig{Json: true, Quiet: true, Verbosity: 3}, zerolog.Disabled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			Setup(&test.config)
			if got := zerolog.GlobalLevel(); got != test.want {
				t.Errorf("level %s, want %s", got, test.want)
			}
		})
	}
}
