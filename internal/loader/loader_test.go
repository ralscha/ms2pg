package loader

import (
	"context"
	"strings"
	"testing"
)

func TestRunnerLoggerDefaultsWhenNil(t *testing.T) {
	runner := Runner{}
	if runner.logger() == nil {
		t.Fatal("logger() returned nil, want default logger")
	}
}

func TestRunnerRejectsInvalidFiltersBeforeConnecting(t *testing.T) {
	runner := Runner{Config: Config{IncludeTables: []string{"[invalid"}}}
	err := runner.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "validate filters") {
		t.Fatalf("Run() error = %v, want filter validation error", err)
	}
}
