// Package timeout is for handling timeout invocation of external command
package timeout

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"syscall"
	"time"

	"github.com/Songmu/wrapcommander"
)

// Timeout is main struct of timeout package
type Timeout struct {
	Duration   time.Duration
	KillAfter  time.Duration
	Signal     os.Signal
	Foreground bool
	Cmd        *exec.Cmd
}

var defaultSignal os.Signal

func init() {
	switch runtime.GOOS {
	case "windows":
		defaultSignal = os.Interrupt
	default:
		defaultSignal = syscall.SIGTERM
	}
}

// exit statuses are same with GNU timeout
const (
	exitNormal     = 0
	exitTimedOut   = 124
	exitUnknownErr = 125
	exitKilled     = 137
)

// Error is error of timeout
type Error struct {
	ExitCode int
	Err      error
}

func (err *Error) Error() string {
	return fmt.Sprintf("exit code: %d, %s", err.ExitCode, err.Err.Error())
}

// ExitStatus stores exit information of the command
type ExitStatus struct {
	Code     int
	Signaled bool
	typ      exitType
}

// IsTimedOut returns the command timed out or not
func (ex ExitStatus) IsTimedOut() bool {
	return ex.typ == exitTypeTimedOut || ex.typ == exitTypeKilled
}

// IsKilled returns the command is killed or not
func (ex ExitStatus) IsKilled() bool {
	return ex.typ == exitTypeKilled
}

// GetExitCode gets the exit code for command line tools
func (ex ExitStatus) GetExitCode() int {
	switch {
	case ex.IsKilled():
		return exitKilled
	case ex.IsTimedOut():
		return exitTimedOut
	default:
		return ex.Code
	}
}

// GetChildExitCode gets the exit code of the Cmd itself
func (ex ExitStatus) GetChildExitCode() int {
	return ex.Code
}

type exitType int

// exit types
const (
	exitTypeNormal exitType = iota
	exitTypeTimedOut
	exitTypeKilled
)

func (tio *Timeout) signal() os.Signal {
	if tio.Signal == nil {
		return defaultSignal
	}
	return tio.Signal
}

// Run is synchronous interface of executing command and returning information
func (tio *Timeout) Run() (ExitStatus, string, string, error) {
	cmd := tio.getCmd()
	var outBuffer, errBuffer bytes.Buffer
	cmd.Stdout = &outBuffer
	cmd.Stderr = &errBuffer

	ch, err := tio.RunCommand()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitStatus{}, string(outBuffer.Bytes()), string(errBuffer.Bytes()), err
	}
	exitSt := <-ch
	return exitSt, string(outBuffer.Bytes()), string(errBuffer.Bytes()), nil
}

// RunSimple executes command and only returns integer as exit code. It is mainly for go-timeout command
func (tio *Timeout) RunSimple(preserveStatus bool) int {
	cmd := tio.getCmd()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUnknownErr
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUnknownErr
	}

	ch, err := tio.RunCommand()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return getExitCodeFromErr(err)
	}

	go func() {
		defer stdoutPipe.Close()
		io.Copy(os.Stdout, stdoutPipe)
	}()

	go func() {
		defer stderrPipe.Close()
		io.Copy(os.Stderr, stderrPipe)
	}()

	exitSt := <-ch
	if preserveStatus {
		return exitSt.GetChildExitCode()
	}
	return exitSt.GetExitCode()
}

func getExitCodeFromErr(err error) int {
	if err != nil {
		if tmerr, ok := err.(*Error); ok {
			return tmerr.ExitCode
		}
		return -1
	}
	return exitNormal
}

// RunCommand is executing the command and handling timeout. This is primitive interface of Timeout
func (tio *Timeout) RunCommand() (chan ExitStatus, error) {
	cmd := tio.getCmd()

	if err := cmd.Start(); err != nil {
		return nil, &Error{
			ExitCode: wrapcommander.ResolveExitCode(err),
			Err:      err,
		}
	}

	exitChan := make(chan ExitStatus)
	go func() {
		exitChan <- tio.handleTimeout()
	}()

	return exitChan, nil
}

func (tio *Timeout) handleTimeout() (ex ExitStatus) {
	cmd := tio.getCmd()
	exitChan := getExitChan(cmd)
	cases := []reflect.SelectCase{
		{ // 0: command exit
			Chan: reflect.ValueOf(exitChan),
			Dir:  reflect.SelectRecv,
		},
		{ // 1: timed out and send signal
			Chan: reflect.ValueOf(time.After(tio.Duration)),
			Dir:  reflect.SelectRecv,
		},
	}
	if tio.KillAfter > 0 {
		// 2: send KILL signal
		cases = append(cases, reflect.SelectCase{
			Chan: reflect.ValueOf(time.After(tio.Duration + tio.KillAfter)),
			Dir:  reflect.SelectRecv,
		})
	}
	for {
		chosen, recv, _ := reflect.Select(cases)
		switch chosen {
		case 0:
			if st, ok := recv.Interface().(syscall.WaitStatus); ok {
				ex.Code = wrapcommander.WaitStatusToExitCode(st)
				ex.Signaled = st.Signaled()
			}
			return ex
		case 1:
			tio.terminate()
			ex.typ = exitTypeTimedOut
		case 2:
			tio.killall()
			// just to make sure
			cmd.Process.Kill()
			ex.typ = exitTypeKilled
		}
	}
}

func getExitChan(cmd *exec.Cmd) chan syscall.WaitStatus {
	ch := make(chan syscall.WaitStatus)
	go func() {
		err := cmd.Wait()
		st, _ := wrapcommander.ErrorToWaitStatus(err)
		ch <- st
	}()
	return ch
}
