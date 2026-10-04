package controllers

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// The Job script is the only thing that reports whether kubectl did what the
// Manifests asked for, so these tests run it rather than read it. They put a
// stub kubectl and a stub pkill on PATH, run the exact Command and Args
// GenerateJob produced, and look at the exit status the controller would see
// as Job.Status.Succeeded.

// stubBin writes an executable shell script called name into dir.
func stubBin(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// runJobScript runs the script of the Job GenerateJob builds for the given
// action, with a kubectl stub that exits kubectlExit for apply and delete, and
// a pkill stub that exits pkillExit. It returns the script's exit status.
func runJobScript(t *testing.T, delete bool, kubectlExit, pkillExit int) int {
	t.Helper()

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}

	ref := "mysecret"
	j, err := GenerateJob(manifests(&ref), delete, "kubectl:latest")
	if err != nil {
		t.Fatalf("GenerateJob: %v", err)
	}
	c := j.Spec.Template.Spec.Containers[0]

	bin := t.TempDir()
	// `kubectl get pods` is the readiness probe the script waits on, and it has
	// to succeed or the test would sit through the 300 second retry loop.
	stubBin(t, bin, "kubectl", `
case "$1" in
  get) exit 0 ;;
esac
exit `+strconv.Itoa(kubectlExit))
	stubBin(t, bin, "pkill", "exit "+strconv.Itoa(pkillExit))

	cmd := exec.Command(c.Command[0], append(append([]string{}, c.Command[1:]...), c.Args...)...)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))

	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("running the job script: %v\n%s", err, out)
	}
	return ee.ExitCode()
}

func TestJobScriptReportsAFailedApply(t *testing.T) {
	if got := runJobScript(t, false, 3, 0); got != 3 {
		t.Fatalf("exit status = %d, want 3: a failed kubectl apply is reported as a finished Job, so the Manifests reports executed", got)
	}
}

func TestJobScriptReportsAFailedDelete(t *testing.T) {
	if got := runJobScript(t, true, 1, 0); got != 1 {
		t.Fatalf("exit status = %d, want 1: a failed kubectl delete is read as finalized, the finalizer is dropped and the remote objects leak", got)
	}
}

func TestJobScriptSucceedsWhenKubectlDoes(t *testing.T) {
	if got := runJobScript(t, false, 0, 0); got != 0 {
		t.Fatalf("exit status = %d, want 0", got)
	}
}

// pkill exits 1 when it matched nothing, which happens whenever the edgevpn
// sidecar has already gone. It runs after kubectl, so it must not become the
// status the Job is judged by.
func TestJobScriptIgnoresThePkillStatus(t *testing.T) {
	if got := runJobScript(t, false, 0, 1); got != 0 {
		t.Fatalf("exit status = %d, want 0: pkill failing to match edgevpn turned a good apply into a failed Job", got)
	}
	if got := runJobScript(t, false, 4, 1); got != 4 {
		t.Fatalf("exit status = %d, want 4: pkill overwrote the kubectl status", got)
	}
}
