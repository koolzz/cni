// Copyright 2026 CNI authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package invoke_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sys/unix"

	"github.com/containernetworking/cni/pkg/invoke"
)

const parentExitHelperEnv = "CNI_TEST_PARENT_EXIT_HELPER"

type parentExitConfig struct {
	PIDFile string
	OptIn   bool
}

// Dispatch subprocess fixtures before the test runner starts. The parent uses
// the real executor; the child blocks until its parent or this test kills it.
func init() {
	role := os.Getenv(parentExitHelperEnv)
	if role == "" {
		return
	}
	var config parentExitConfig
	if err := json.NewDecoder(os.Stdin).Decode(&config); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if role == "child" {
		if err := os.WriteFile(config.PIDFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if role != "parent" {
		os.Exit(1)
	}
	binary, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	input, err := json.Marshal(config)
	if err != nil {
		os.Exit(1)
	}
	executor := &invoke.RawExec{KillOnParentExit: config.OptIn}
	_, err = executor.ExecPlugin(context.Background(), binary, input,
		append(os.Environ(), parentExitHelperEnv+"=child"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

var _ = Describe("plugin lifetime on Linux", func() {
	DescribeTable("preserves the default and supports explicit parent-bound execution", func(optIn bool) {
		// Adopt and reap only our own orphaned fixture after terminating its
		// parent. Restore the original setting when this spec is done.
		var original int32
		_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, unix.PR_GET_CHILD_SUBREAPER,
			uintptr(unsafe.Pointer(&original)), 0, 0, 0, 0)
		Expect(errno).To(BeZero())
		Expect(unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)).To(Succeed())
		DeferCleanup(func() {
			Expect(unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, uintptr(original), 0, 0, 0)).To(Succeed())
		})
		pidFile := GinkgoT().TempDir() + "/child.pid"
		binary, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		input, err := json.Marshal(parentExitConfig{PIDFile: pidFile, OptIn: optIn})
		Expect(err).NotTo(HaveOccurred())
		parent := exec.Command(binary)
		parent.Env = append(os.Environ(), parentExitHelperEnv+"=parent")
		parent.Stdin = bytes.NewReader(input)
		Expect(parent.Start()).To(Succeed())
		parentDone := make(chan error, 1)
		go func() { parentDone <- parent.Wait() }()
		parentReaped := false
		DeferCleanup(func() {
			_ = parent.Process.Kill()
			if !parentReaped {
				Eventually(parentDone, 5*time.Second).Should(Receive())
			}
		})
		var childPID int
		Eventually(func() bool {
			data, readErr := os.ReadFile(pidFile)
			if readErr != nil {
				return false
			}
			childPID, readErr = strconv.Atoi(string(data))
			return readErr == nil && childPID > 1
		}, 5*time.Second).Should(BeTrue())
		child, err := os.FindProcess(childPID)
		Expect(err).NotTo(HaveOccurred())
		childReaped := false
		childWaitStarted := false
		childDone := make(chan *os.ProcessState, 1)
		DeferCleanup(func() {
			_ = child.Kill()
			// Killing/reaping the parent first makes the child ours to reap.
			_ = parent.Process.Kill()
			if !parentReaped {
				Eventually(parentDone, 5*time.Second).Should(Receive())
				parentReaped = true
			}
			if !childReaped {
				if childWaitStarted {
					Eventually(childDone, 5*time.Second).Should(Receive())
				} else {
					_, _ = child.Wait()
				}
			}
			_ = child.Release()
		})
		// A healthy caller must not lose its plugin while waiting for output.
		Consistently(parentDone, 100*time.Millisecond).ShouldNot(Receive())
		Expect(parent.Process.Kill()).To(Succeed())
		Eventually(parentDone, 5*time.Second).Should(Receive(HaveOccurred()))
		parentReaped = true
		childWaitStarted = true
		go func() {
			state, _ := child.Wait()
			childDone <- state
		}()
		if !optIn {
			Consistently(childDone, 100*time.Millisecond).ShouldNot(Receive())
			Expect(child.Kill()).To(Succeed())
		}
		var state *os.ProcessState
		Eventually(childDone, 5*time.Second).Should(Receive(&state))
		childReaped = true
		Expect(state).NotTo(BeNil())
		status := state.Sys().(syscall.WaitStatus)
		Expect(status.Signaled()).To(BeTrue())
		Expect(status.Signal()).To(Equal(syscall.SIGKILL))
	},
		Entry("legacy execution can outlive its caller", false),
		Entry("opt-in execution terminates with its caller", true),
	)
})
