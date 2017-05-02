package centrifuge

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// closeErr tries to extract connection close code and reason from error.
// It returns true as first return value in case of successful extraction.
func closeErr(err error) (bool, int, string) {
	if closeErr, ok := err.(*websocket.CloseError); ok {
		return true, closeErr.Code, closeErr.Text
	}
	return false, 0, ""
}

type connection interface {
	Close()
	WriteMessage([]byte) error
	ReadMessage() ([]byte, error)
}

type wsConn struct {
	conn         *websocket.Conn
	writeTimeout time.Duration
}

type connFactory func(string, time.Duration, bool) (connection, error)

func newWSConnection(url string, writeTimeout time.Duration, skipVerify bool) (connection, error) {
	wsHeaders := http.Header{}
	dialer := websocket.DefaultDialer
	if skipVerify {
		dialer.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}
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
