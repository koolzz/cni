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

package invoke

import (
	"os/exec"
	"runtime"
	"syscall"
)

func runPlugin(cmd *exec.Cmd, killOnParentExit bool) error {
	if !killOnParentExit {
		return cmd.Run()
	}
	// PDEATHSIG follows the spawning task, not just its thread group. Keep
	// that OS thread alive until the plugin exits, including while Go parks
	// this goroutine waiting for the process or its output pipes.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return cmd.Run()
}
