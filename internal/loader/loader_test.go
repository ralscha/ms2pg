package loader

import "testing"

func TestRunnerLoggerDefaultsWhenNil(t *testing.T) {
	runner := Runner{}
	if runner.logger() == nil {
		t.Fatal("logger() returned nil, want default logger")
	}
}
