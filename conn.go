package centrifuge

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

type Conn interface {
	Close()
	WriteMessage([]byte) error
	ReadMessage() ([]byte, error)
}

type wsConn struct {
	conn         *websocket.Conn
	writeTimeout time.Duration
}

type ConnFactory func(string, time.Duration) (Conn, error)

func NewWSConnection(url string, writeTimeout time.Duration) (Conn, error) {
	wsHeaders := http.Header{}
	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial(url, wsHeaders)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("Wrong status code while connecting to server: '%d'", resp.StatusCode)
	}
	return &wsConn{conn: conn, writeTimeout: writeTimeout}, nil
}

func (c *wsConn) Close() {
	c.conn.Close()
}

func (c *wsConn) WriteMessage(msg []byte) error {
	c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	err := c.conn.WriteMessage(websocket.TextMessage, msg)
	c.conn.SetWriteDeadline(time.Time{})
	return err
}

func (c *wsConn) ReadMessage() ([]byte, error) {
	_, message, err := c.conn.ReadMessage()
	return message, err
}
