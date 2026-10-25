package light

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// DefaultSocket is where the daemon listens; KBDLIGHT_SOCKET overrides it.
const DefaultSocket = "/run/kbdlight.sock"

// Request / Response: one JSON line each way over the socket.
type Request struct {
	Cmd    string  `json:"cmd"` // status or set
	Config *Config `json:"config,omitempty"`
}

type Response struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Config Config `json:"config"`
	Value  int    `json:"value"` // the intensity being shown right now
}

func Socket() string {
	if s := os.Getenv("KBDLIGHT_SOCKET"); s != "" {
		return s
	}
	return DefaultSocket
}

// Call sends one request to the daemon and returns its answer.
func Call(req Request) (Response, error) {
	var r Response
	conn, err := net.DialTimeout("unix", Socket(), 2*time.Second)
	if err != nil {
		return r, fmt.Errorf("the kbdlight daemon is not running (systemctl start kbdlight): %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return r, err
	}
	if err := json.NewDecoder(conn).Decode(&r); err != nil {
		return r, err
	}
	if !r.OK {
		return r, fmt.Errorf("%s", r.Error)
	}
	return r, nil
}

func Status() (Response, error) { return Call(Request{Cmd: "status"}) }

func Set(c Config) (Response, error) { return Call(Request{Cmd: "set", Config: &c}) }
