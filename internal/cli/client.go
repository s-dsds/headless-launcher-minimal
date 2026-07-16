package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"

	"headless-launcher-go/internal/ipc"
)

// connect sends a message to the IPC server and processes responses.
func connect(msgType string, data any, handler func(ipc.Envelope) bool) error {
	conn, err := net.Dial("unix", ipc.SocketPath())
	if err != nil {
		return fmt.Errorf("connection error: %w", err)
	}
	c := ipc.NewConn(conn)
	defer c.Close()

	if err := c.Send(msgType, data); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	for {
		env, err := c.Receive()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if handler != nil && !handler(env) {
			return nil
		}
	}
}

// defaultHandler prints "message" events to stdout and returns true to continue.
func defaultHandler(env ipc.Envelope) bool {
	switch env.Type {
	case "message":
		var msg string
		if err := json.Unmarshal(env.Data, &msg); err != nil {
			log.Println("unmarshal message:", err)
			return true
		}
		fmt.Println(msg)
	default:
		fmt.Printf("[%s] %s\n", env.Type, string(env.Data))
	}
	return true
}
