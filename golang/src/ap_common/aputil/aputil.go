/*
 * COPYRIGHT 2017 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 */

package aputil

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"syscall"
)

// Child is used to build and track the state of an child subprocess
type Child struct {
	Cmd     *exec.Cmd
	Process *os.Process

	pipes  int
	done   chan bool
	logger *log.Logger
	prefix string
}

//
// Wait for stdout/stderr from a process, and print whatever it sends.  When the
// pipe is closed, notify our caller.
//
func handlePipe(c *Child, r io.ReadCloser) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if c.logger != nil {
			c.logger.Printf("%s%s\n", c.prefix, scanner.Text())
		} else {
			fmt.Printf("%s\n", scanner.Text())
		}
	}

	c.done <- true
}

// Start launches a prepared child process
func (c *Child) Start() error {
	err := c.Cmd.Start()
	if err == nil {
		c.Process = c.Cmd.Process
	}
	return err
}

// Wait waits for the child process to exit.  If we are capturing its output, we
// will wait for the stdin/stderr pipes to be closed.
func (c *Child) Wait() error {
	// Wait for the stdout/stderr pipes to close and for the child
	// process to exit
	for c.pipes > 0 {
		<-c.done
		c.pipes--
	}
	return c.Cmd.Wait()
}

// SetUID allows us to launch a child process with different credentials than
// the launching daemon.
func (c *Child) SetUID(uid, gid uint32) {
	cred := syscall.Credential{
		Uid: uid,
		Gid: gid,
	}

	attr := syscall.SysProcAttr{
		Credential: &cred,
	}

	c.Cmd.SysProcAttr = &attr
}

// LogOutput will cause us to capture the stdin/stdout streams from a child
// process
func (c *Child) LogOutput(prefix string, flags int) {
	c.logger = log.New(os.Stderr, "", flags)
	c.prefix = prefix

	c.pipes = 0
	c.done = make(chan bool)
	if stdout, err := c.Cmd.StdoutPipe(); err == nil {
		c.pipes++
		go handlePipe(c, stdout)
	}
	if stderr, err := c.Cmd.StderrPipe(); err == nil {
		c.pipes++
		go handlePipe(c, stderr)
	}
}

// NewChild instantiates the tracking structure for a child process
func NewChild(execpath string, args ...string) *Child {
	var c Child

	c.Cmd = exec.Command(execpath, args...)

	return &c
}

// FileExists checks to see whether the file/directory at the path location
// exists
func FileExists(filename string) bool {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return false
	}
	return true
}
