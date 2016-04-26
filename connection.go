package centrifuge

import (
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"time"
)

type Connection interface {
	Close()
	WriteMessage([]byte) error
	ReadMessage() ([]byte, error)
}

type ConnectionFactory func(string, time.Duration) (Connection, error)

// ------------------------
// Websocket implementation
// ------------------------

type wsConnection struct {
	conn         *websocket.Conn
	writeTimeout time.Duration
}

func NewWSConnection(url string, writeTimeout time.Duration) (Connection, error) {
	wsHeaders := http.Header{}
	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial(url, wsHeaders)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("Wrong status code while connecting to server: '%d'", resp.StatusCode)
	}
	return &wsConnection{conn: conn, writeTimeout: writeTimeout}, nil
}

func (c *wsConnection) Close() {
	c.conn.Close()
}

func (c *wsConnection) WriteMessage(msg []byte) error {
	c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	err := c.conn.WriteMessage(websocket.TextMessage, msg)
	c.conn.SetWriteDeadline(time.Time{})
	return err
}

func (c *wsConnection) ReadMessage() ([]byte, error) {
	_, message, err := c.conn.ReadMessage()
	return message, err
}
