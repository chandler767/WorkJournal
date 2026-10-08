// Package capture supervises the Swift recorder process and talks to its control socket.
package capture

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"time"
)

// Status is the recorder's reply to the "status" command.
type Status struct {
	State   string    `json:"state"`
	Since   time.Time `json:"since"`
	Meeting bool      `json:"meeting"`
	Screens bool      `json:"screens"`
	Audio   bool      `json:"audio"`
}

// Control sends commands to a running recorder.
type Control interface {
	Pause() error
	Resume() error
	Status() (Status, error)
	// Send issues a command that is answered with "ok" (e.g. screens_off, quit).
	Send(cmd string) error
	// Rotate finishes the current audio chunks (both tracks) and starts new
	// ones; it returns once the finished files are fully written.
	Rotate() error
}

// commandTimeout bounds dialing and answering an ordinary command;
// rotateTimeout allows for the recorder finishing its audio files.
var (
	commandTimeout = 3 * time.Second
	rotateTimeout  = 10 * time.Second
)

type socketControl struct{ sock string }

// NewSocketControl returns a Control that dials the unix socket for every call.
func NewSocketControl(sock string) Control { return socketControl{sock: sock} }

func (c socketControl) roundTrip(cmd string) (string, error) {
	return roundTrip(c.sock, cmd, commandTimeout)
}

func roundTrip(sock, cmd string, timeout time.Duration) (string, error) {
	conn, err := net.DialTimeout("unix", sock, timeout)
	if err != nil {
		return "", fmt.Errorf("dial recorder: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return "", err
	}
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		return "", fmt.Errorf("send %q: %w", cmd, err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read reply to %q: %w", cmd, err)
	}
	return strings.TrimSpace(line), nil
}

func (c socketControl) Send(cmd string) error { return c.sendOK(cmd, commandTimeout) }

func (c socketControl) sendOK(cmd string, timeout time.Duration) error {
	reply, err := roundTrip(c.sock, cmd, timeout)
	if err != nil {
		return err
	}
	if reply != "ok" {
		return fmt.Errorf("recorder %q: %s", cmd, reply)
	}
	return nil
}

func (c socketControl) Rotate() error { return c.sendOK("rotate", rotateTimeout) }

func (c socketControl) Pause() error  { return c.Send("pause") }
func (c socketControl) Resume() error { return c.Send("resume") }

func (c socketControl) Status() (Status, error) {
	reply, err := c.roundTrip("status")
	if err != nil {
		return Status{}, err
	}
	var st Status
	if err := json.Unmarshal([]byte(reply), &st); err != nil {
		return Status{}, fmt.Errorf("parse status %q: %w", reply, err)
	}
	return st, nil
}

// QuitRunning stops a recorder that is already serving sock (one orphaned by
// an earlier `journal serve`), so two recorders never record at once. It
// probes with "status" (probe timeout); when a recorder answers it sends
// "quit" and waits up to gone for the socket file to disappear. found
// reports whether a recorder answered.
func QuitRunning(sock string, probe, gone time.Duration) (found bool, err error) {
	if _, err := roundTrip(sock, "status", probe); err != nil {
		return false, nil // nobody listening (or a stale socket file)
	}
	reply, err := roundTrip(sock, "quit", probe)
	if err != nil {
		return true, fmt.Errorf("quit running recorder: %w", err)
	}
	if reply != "ok" {
		return true, fmt.Errorf("quit running recorder: %s", reply)
	}
	deadline := time.Now().Add(gone)
	for {
		if _, err := os.Stat(sock); errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		if time.Now().After(deadline) {
			return true, fmt.Errorf("running recorder did not exit within %s", gone)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
