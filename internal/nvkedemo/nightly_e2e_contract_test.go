// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

func TestNightlyAuthoredRuntimeStepConsumesActualCycleWriter(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/nvkr-nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run string }
		}
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	runtimeStepShell := ""
	for _, step := range workflow.Jobs["runtime-test"].Steps {
		if step.Name == "Authenticate the publication and run the NVKR Go VM smoke test" {
			if runtimeStepShell != "" {
				t.Fatal("ambiguous authored runtime step")
			}
			runtimeStepShell = step.Run
		}
	}
	if runtimeStepShell == "" {
		t.Fatal("missing authored runtime step")
	}
	// Only Go is substituted. Execute the entire authored step, including its
	// list check and real jq postcondition. A zero-exit skipped Go invocation
	// alone must never satisfy the authored step's successful-cycle check.
	goFixture := `go() {
  case "$*" in
    *"-list "*) printf '%s\n' TestNightlyPublishedCandidate ;;
    *"-run "*) printf '%s\n' "$FIXTURE_GO_RESULT" ;;
    *) return 91 ;;
  esac
}
`
	for _, test := range []struct {
		name, phase, envKey, envValue string
		result                        error
		skip                          bool
		edit                          func(map[string]any)
		pass                          bool
	}{
		{name: "successful writer", phase: "runtime", pass: true},
		{name: "successful Go skip without cycle", skip: true},
		{name: "wrong profile", phase: "runtime", envKey: "NVKR_NIGHTLY_PROFILE", envValue: "other"},
		{name: "wrong source", phase: "runtime", envKey: "GITHUB_SHA", envValue: strings.Repeat("b", 40)},
		{name: "wrong NVKE main source", phase: "runtime", envKey: "NVKR_NVKE_SOURCE_COMMIT", envValue: strings.Repeat("d", 40)},
		{name: "wrong phase", phase: "preparation"},
		{name: "wrong coordinator run", phase: "runtime", envKey: "GITHUB_RUN_ID", envValue: "124"},
		{name: "wrong coordinator attempt", phase: "runtime", envKey: "GITHUB_RUN_ATTEMPT", envValue: "3"},
		{name: "failed runtime or cleanup", phase: "runtime", result: errors.New("joined cleanup failed")},
		{name: "wrong authority", phase: "runtime", edit: func(cycle map[string]any) { cycle["qualificationAuthority"] = "qualified" }},
		{name: "numeric coordinator run", phase: "runtime", edit: func(cycle map[string]any) { cycle["coordinatorRun"] = 123 }},
		{name: "numeric coordinator attempt", phase: "runtime", edit: func(cycle map[string]any) { cycle["coordinatorAttempt"] = 2 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			public := t.TempDir()
			profile := NightlyProfile{Name: "fixture-profile"}
			build := NightlyBuild{CoordinatorRun: "123", CoordinatorAttempt: "2", ProducerRun: 456, ProducerAttempt: 1, SourceCommit: strings.Repeat("a", 40), NVKECommit: strings.Repeat("c", 40)}
			if !test.skip {
				if err := writeNightlyCycle(t.Context(), public, profile, build, "fixture-subject", test.phase, test.result); err != nil {
					t.Fatal(err)
				}
				if test.edit != nil {
					path := filepath.Join(public, "cycle.json")
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					var cycle map[string]any
					if err := json.Unmarshal(raw, &cycle); err != nil {
						t.Fatal(err)
					}
					test.edit(cycle)
					raw, err = json.Marshal(cycle)
					if err != nil {
						t.Fatal(err)
					}
					if err := privateReplace(path, raw); err != nil {
						t.Fatal(err)
					}
				}
			}
			values := map[string]string{"PATH": os.Getenv("PATH"), "NVKR_NIGHTLY_REPORTS": public,
				"NVKR_NIGHTLY_PROFILE": profile.Name, "GITHUB_RUN_ID": build.CoordinatorRun,
				"GITHUB_RUN_ATTEMPT": build.CoordinatorAttempt, "GITHUB_SHA": build.SourceCommit,
				"NVKR_NVKE_SOURCE_COMMIT":         build.NVKECommit,
				"NVKR_NIGHTLY_JOB_DEADLINE_EPOCH": fmt.Sprint(time.Now().Add(330 * time.Minute).Unix()),
				"FIXTURE_GO_RESULT":               "--- PASS: TestNightlyPublishedCandidate"}
			if test.skip {
				values["FIXTURE_GO_RESULT"] = "--- SKIP: TestNightlyPublishedCandidate"
			}
			if test.envKey != "" {
				values[test.envKey] = test.envValue
			}
			env := SetEnv(nil, values)
			bash, err := commandPath("bash", env)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := commandPath("jq", env); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bash, "-c", goFixture+runtimeStepShell)
			command.Env = env
			if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
				t.Fatal(err)
			}
			output, err := command.CombinedOutput()
			if (err == nil) != test.pass {
				t.Fatalf("authored runtime step pass=%t, want %t: %v\n%s", err == nil, test.pass, err, output)
			}
		})
	}
}

func TestNightlyCycleWriterRejectsUnavailableContextAndDestination(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"nil context", nil, e.CodeUsage},
		{"canceled context", ctx, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			public := t.TempDir()
			assertNightlyCode(t, writeNightlyCycle(test.ctx, public, NightlyProfile{}, NightlyBuild{}, "", "runtime", nil), test.code)
			if _, err := os.Stat(filepath.Join(public, "cycle.json")); !os.IsNotExist(err) {
				t.Fatal("refused writer exposed cycle", err)
			}
		})
	}
	assertNightlyCode(t, writeNightlyCycle(t.Context(), filepath.Join(t.TempDir(), "missing"), NightlyProfile{}, NightlyBuild{}, "", "runtime", nil), e.CodeImmutableViolation)
}

func TestNightlyDeadlineReservesEveryCleanupStage(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name     string
		deadline time.Time
		work     time.Duration
		invalid  bool
	}{
		{"workflow330minutes", now.Add(330 * time.Minute), nightlyMaximumWork, false},
		{"longer enclosing timeout caps work", now.Add(360 * time.Minute), nightlyMaximumWork, false},
		{"shorter enclosing timeout", now.Add(130 * time.Minute), 5 * time.Minute, false},
		{"no test timeout", time.Time{}, nightlyMaximumWork, false},
		{"cleanup would consume entire deadline", now.Add(nightlyCleanupReserve), 0, true},
		{"expired deadline", now.Add(-time.Second), 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget, err := nightlyTimeBudget(now, test.deadline)
			if test.invalid {
				assertNightlyCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil || budget.WorkDeadline.Sub(now) != test.work {
				t.Fatal(budget, err)
			}
			cleanup := nightlyNativeCleanup + nightlyReportGrace
			if budget.WorkDeadline.Add(cleanup).After(budget.CleanupDeadline) || !test.deadline.IsZero() && !budget.CleanupDeadline.Before(test.deadline) {
				t.Fatal("work, process join and cleanup exceed enclosing deadline")
			}
			parent, cancel := context.WithCancel(t.Context())
			cancel()
			bounded, stop := budget.cleanupContext(parent, time.Minute)
			defer stop()
			deadline, ok := bounded.Deadline()
			if !ok || deadline.After(budget.CleanupDeadline) || bounded.Err() != nil {
				t.Fatal("cleanup lost its explicit budget or inherited work cancellation")
			}
		})
	}
}

func TestNightlyOptInCannotSilentlySkipRuntimeJob(t *testing.T) {
	for _, test := range []struct {
		enable, actions, profile string
		enabled, refused         bool
	}{
		{"", "", "", false, false},
		{"", "true", "", false, false},
		{"", "true", "selected-profile", false, true},
		{"0", "true", "selected-profile", false, true},
		{"1", "true", "selected-profile", true, false},
	} {
		enabled, err := nightlyTestAdmission(test.enable, test.actions, test.profile)
		if enabled != test.enabled {
			t.Fatal(test, enabled)
		}
		if test.refused {
			assertNightlyCode(t, err, e.CodeUsage)
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestNightlySignalsCancelTheOwnedTestContext(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range []os.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestNightlySignalContextHelper$", "-test.timeout=8s")
			command.Env = []string{"NVKR_NIGHTLY_TEST_SIGNAL=1"}
			if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
				t.Fatal(err)
			}
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); _ = ownedcmd.KillDescendants(command) }()
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "nightly-signal-ready" {
				cancel()
				_ = command.Wait()
				t.Fatal("signal helper did not start", scanner.Err())
			}
			if err := command.Process.Signal(signal); err != nil {
				cancel()
				_ = command.Wait()
				t.Fatal(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatal("signal did not reach orderly context cancellation", err)
			}
		})
	}
}

func TestNightlySignalContextHelper(t *testing.T) {
	if os.Getenv("NVKR_NIGHTLY_TEST_SIGNAL") != "1" {
		t.Skip("owned signal subprocess fixture")
	}
	ctx, cancel := nightlySignalContext(t.Context(), time.Now().Add(6*time.Second))
	defer cancel()
	fmt.Println("nightly-signal-ready")
	<-ctx.Done()
	if ctx.Err() != context.Canceled {
		t.Fatal("signal was not received", ctx.Err())
	}
}

func TestNightlyPreparationCachesArePrivateAndCredentialsExcluded(t *testing.T) {
	t.Setenv("GH_TOKEN", "must-not-propagate")
	t.Setenv("GITHUB_TOKEN", "must-not-propagate")
	work := t.TempDir()
	binding := map[string]string{"HOME": "/home/runtime", "XDG_RUNTIME_DIR": "/run/user/123"}
	env, err := nightlyPreparationEnvironment(NativeEnvironment(), binding, work)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"HOME": binding["HOME"], "XDG_RUNTIME_DIR": binding["XDG_RUNTIME_DIR"], "GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off", "GOFLAGS": "-mod=readonly", "GH_TOKEN": "", "GITHUB_TOKEN": ""} {
		if got := environmentValue(env, key); got != want {
			t.Fatalf("%s=%q; want %q", key, got, want)
		}
	}
	for _, name := range []string{"go-cache", "go-modules", "go-path"} {
		info, err := os.Stat(filepath.Join(work, name))
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal(name, info, err)
		}
	}
	_, err = nightlyPreparationEnvironment(NativeEnvironment(), binding, work)
	assertNightlyCode(t, err, e.CodeImmutableViolation)
	_, err = nightlyPreparationEnvironment(nil, map[string]string{}, t.TempDir())
	assertNightlyCode(t, err, e.CodeEngineCapability)
}

func TestNightlyCycleKeepsSuiteReportsPrivate(t *testing.T) {
	private, public := t.TempDir(), t.TempDir()
	for name, data := range map[string]string{"junit.xml": "<testsuite>private diagnostic</testsuite>", "candidate-outcome.json": "{}", "SAFE_TO_UPLOAD": "", "private-log": "private fixture"} {
		if err := privateWrite(filepath.Join(private, name), []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeNightlyCycle(t.Context(), public, NightlyProfile{Name: "fixture-profile"}, NightlyBuild{}, "fixture-subject", "runtime", nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(public)
	if err != nil || len(entries) != 1 || entries[0].Name() != "cycle.json" {
		t.Fatal("public attachments must contain only the parent-authored cycle", entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(public, "cycle.json"))
	if err != nil || bytes.Contains(raw, []byte("private diagnostic")) {
		t.Fatal("private suite data reached the public cycle", err)
	}
}
