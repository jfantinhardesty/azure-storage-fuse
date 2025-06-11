//go:build linux

/*
    _____           _____   _____   ____          ______  _____  ------
   |     |  |      |     | |     | |     |     | |       |            |
   |     |  |      |     | |     | |     |     | |       |            |
   | --- |  |      |     | |-----| |---- |     | |-----| |-----  ------
   |     |  |      |     | |     | |     |     |       | |       |
   | ____|  |_____ | ____| | ____| |     |_____|  _____| |_____  |_____


   Licensed under the MIT License <http://opensource.org/licenses/MIT>.

   Copyright © 2020-2025 Microsoft Corporation. All rights reserved.
   Author : <blobfusedev@microsoft.com>

   Permission is hereby granted, free of charge, to any person obtaining a copy
   of this software and associated documentation files (the "Software"), to deal
   in the Software without restriction, including without limitation the rights
   to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
   copies of the Software, and to permit persons to whom the Software is
   furnished to do so, subject to the following conditions:

   The above copyright notice and this permission notice shall be included in all
   copies or substantial portions of the Software.

   THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
   IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
   FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
   AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
   LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
   OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
   SOFTWARE
*/

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Azure/azure-storage-fuse/v2/common/config"
	"github.com/Azure/azure-storage-fuse/v2/common/log"
	"github.com/Azure/azure-storage-fuse/v2/internal"
	"github.com/sevlyar/go-daemon"
)

func createDaemon(
	pipeline *internal.Pipeline,
	ctx context.Context,
	pidFileName string,
	fname string,
) error {
	dmnCtx := &daemon.Context{
		PidFileName: pidFileName,
		PidFilePerm: 0644,
		Umask:       022,
		LogFileName: fname, // this will redirect stderr of child to given file
	}

	// Signal handlers for parent and child to communicate success or failures in mount
	var sigusr2 chan os.Signal
	if !daemon.WasReborn() { // execute in parent only
		sigusr2 = make(chan os.Signal, 1)
		signal.Notify(sigusr2, syscall.SIGUSR2)

	} else { // execute in child only
		daemon.SetSigHandler(sigusrHandler(pipeline, ctx), syscall.SIGUSR1, syscall.SIGUSR2)
		go func() {
			_ = daemon.ServeSignals()
		}()
	}

retry:
	// If the .pid file is locked and there no blobfuse process owning it then we need to try
	// a cleanup of the .pid file. If cleanup goes through then retry the daemonization.
	child, err := dmnCtx.Reborn()
	if err != nil {
		log.Err("mount : failed to daemonize application [%s], trying auto cleanup", err.Error())
		rmErr := os.Remove(pidFileName)
		if rmErr != nil {
			log.Err("mount : auto cleanup failed [%v]", rmErr.Error())
			return Destroy(fmt.Sprintf("failed to daemonize application [%s]", err.Error()))
		}
		goto retry
	}

	log.Debug("mount: foreground disabled, child = %v", daemon.WasReborn())
	if child == nil { // execute in child only
		defer dmnCtx.Release() // nolint
		setGOConfig()
		go startDynamicProfiler()

		// In case of failure stderr will have the error emitted by child and parent will read
		// those logs from the file set in daemon context
		return runPipeline(pipeline, ctx)
	} else { // execute in parent only
		defer os.Remove(fname)

		childDone := make(chan struct{})

		go monitorChild(child.Pid, childDone)

		select {
		case <-sigusr2:
			log.Info("mount: Child [%v] mounted successfully at %s", child.Pid, options.MountPath)

		case <-childDone:
			// Get error string from the child, stderr or child was redirected to a file
			log.Info("mount: Child [%v] terminated from %s", child.Pid, options.MountPath)

			buff, err := os.ReadFile(dmnCtx.LogFileName)
			if err != nil {
				log.Err("mount: failed to read child [%v] failure logs [%s]", child.Pid, err.Error())
				return Destroy(fmt.Sprintf("failed to mount, please check logs [%s]", err.Error()))
			} else {
				return Destroy(string(buff))
			}

		case <-time.After(options.WaitForMount):
			log.Info("mount: Child [%v : %s] status check timeout", child.Pid, options.MountPath)
		}

		_ = log.Destroy()
	}
	return nil
}

func monitorChild(pid int, done chan struct{}) {
	// Monitor the child process and if child terminates then exit
	var wstatus syscall.WaitStatus

	for {
		// Wait for a signal from child
		wpid, err := syscall.Wait4(pid, &wstatus, 0, nil)
		if err != nil {
			log.Err("Error retrieving child status [%s]", err.Error())
			break
		}

		if wpid == pid {
			// Exit only if child has exited
			// Signal can be received on a state change of child as well
			if wstatus.Exited() || wstatus.Signaled() || wstatus.Stopped() {
				close(done)
				return
			}
		}
	}
}

func sigusrHandler(pipeline *internal.Pipeline, ctx context.Context) daemon.SignalHandlerFunc {
	return func(sig os.Signal) error {
		log.Crit("Mount::sigusrHandler : Signal %d received", sig)

		var err error
		if sig == syscall.SIGUSR1 {
			log.Crit("Mount::sigusrHandler : SIGUSR1 received")
			config.OnConfigChange()
		}

		return err
	}
}
