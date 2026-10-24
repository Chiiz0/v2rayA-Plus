package main

import (
	"errors"
	"testing"
)

func TestWaitForShutdownAlwaysCleansUp(t *testing.T) {
	bindErr := errors.New("listen: address already in use")
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "signal"},
		{name: "HTTP bind failure", err: bindErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			errch := make(chan error, 1)
			errch <- tt.err
			cleanups := 0
			err := waitForShutdown(errch, func() { cleanups++ })
			if !errors.Is(err, tt.err) {
				t.Fatalf("shutdown returned %v, want %v", err, tt.err)
			}
			if cleanups != 1 {
				t.Fatalf("cleanup ran %d times, want once before returning", cleanups)
			}
		})
	}
}
