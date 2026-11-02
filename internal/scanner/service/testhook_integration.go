//go:build integration && linux

package service

import (
	"os"
	"time"
)

// pauseEnv names the phase in which the service sleeps, so an integration test
// can kill it there deterministically:
//
//   - "walk": after the generation file is created, before the walk starts;
//   - "finalize": after the walk ended and the phase was written to the job;
//   - "publish": after the phase was written, before the publish transaction.
//
// This file is built only with the "integration" tag, so release binaries
// never read the variable.
const (
	pauseEnv = "HOGWATCH_TEST_PAUSE_PHASE"
	pauseFor = 5 * time.Second
)

func init() {
	applyTestHooks = func(h *hooks) {
		switch p := os.Getenv(pauseEnv); p {
		case "walk", "finalize":
			h.onPhase = func(phase string) {
				if phase == p {
					time.Sleep(pauseFor)
				}
			}
		case "publish":
			h.beforePublish = func() { time.Sleep(pauseFor) }
		}
	}
}
