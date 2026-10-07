// Package capture supervises the Swift recorder process and talks to its control socket.
package capture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
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
}

const dialTimeout = 3 * time.Second

type socketControl struct{ sock string }

// NewSocketControl returns a Control that dials the unix socket for every call.
func NewSocketControl(sock string) Control { return socketControl{sock: sock} }

func (c socketControl) roundTrip(cmd string) (string, error) {
	conn, err := net.DialTimeout("unix", c.sock, dialTimeout)
	if err != nil {
		return "", fmt.Errorf("dial recorder: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
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

func (c socketControl) Send(cmd string) error {
	reply, err := c.roundTrip(cmd)
	if err != nil {
		return err
	}
	if reply != "ok" {
		return fmt.Errorf("recorder %q: %s", cmd, reply)
	}
	return nil
}

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
