package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
)

const Delimiter = '\f' // form-feed, matches node-ipc default

// Envelope is the wire format: {"type":"<event>","data":<json>}\f
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Conn wraps a net.Conn with framed read/write.
type Conn struct {
	raw     net.Conn
	scanner *bufio.Scanner
}

func NewConn(c net.Conn) *Conn {
	s := bufio.NewScanner(c)
	s.Buffer(make([]byte, 0, 4*1024*1024), 16*1024*1024)
	s.Split(splitOnFormFeed)
	return &Conn{raw: c, scanner: s}
}

// Send writes a framed message.
func (c *Conn) Send(msgType string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal data: %w", err)
	}
	env := Envelope{Type: msgType, Data: raw}
	b, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	b = append(b, Delimiter)
	_, err = c.raw.Write(b)
	return err
}

// SendRaw sends a pre-built envelope.
func (c *Conn) SendRaw(env Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	b = append(b, Delimiter)
	_, err = c.raw.Write(b)
	return err
}

// Receive reads the next framed message. Returns io.EOF at end.
func (c *Conn) Receive() (Envelope, error) {
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return Envelope{}, err
		}
		return Envelope{}, io.EOF
	}
	var env Envelope
	if err := json.Unmarshal(c.scanner.Bytes(), &env); err != nil {
		return Envelope{}, fmt.Errorf("unmarshal: %w", err)
	}
	return env, nil
}

// Close closes the underlying connection.
func (c *Conn) Close() error {
	return c.raw.Close()
}

// NetConn returns the underlying net.Conn.
func (c *Conn) NetConn() net.Conn {
	return c.raw
}

// splitOnFormFeed is a bufio.SplitFunc that splits on \f.
func splitOnFormFeed(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i < len(data); i++ {
		if data[i] == Delimiter {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// SocketPath returns the default node-ipc socket path.
func SocketPath() string {
	return "/tmp/app.wlserver"
}
