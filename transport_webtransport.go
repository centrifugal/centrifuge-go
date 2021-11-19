package centrifuge

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lucas-clemente/quic-go/http3"

	"github.com/centrifugal/protocol"
	"github.com/lucas-clemente/quic-go"
)

type webTransport struct {
	mu             sync.Mutex
	stream         quic.Stream
	webTransport   http3.WebTransport
	protocolType   protocol.Type
	commandEncoder protocol.CommandEncoder
	replyCh        chan *protocol.Reply
	config         webTransportConfig
	disconnect     *disconnect
	closed         bool
	closeCh        chan struct{}
}

type webTransportConfig struct {
	TLSConfig          *tls.Config
	QUICConfig         *quic.Config
	DisableCompression bool
	EnableDatagrams    bool
}

func newWebTransport(url string, protocolType protocol.Type, config webTransportConfig) (transport, error) {
	client := &http.Client{
		Transport: &http3.RoundTripper{
			TLSClientConfig:    config.TLSConfig,
			DisableCompression: config.DisableCompression,
			QuicConfig:         config.QUICConfig,
			EnableDatagrams:    config.EnableDatagrams,
			EnableWebTransport: true,
		},
	}

	res, err := client.Get(url)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wrong status code while connecting to server: %d", res.StatusCode)
	}

	wt, err := res.Body.(http3.WebTransporter).WebTransport()
	if err != nil {
		return nil, err
	}

	stream, err := wt.OpenStream()
	if err != nil {
		return nil, err
	}

	t := &webTransport{
		stream:         stream,
		webTransport:   wt,
		replyCh:        make(chan *protocol.Reply, 128),
		config:         config,
		closeCh:        make(chan struct{}),
		commandEncoder: newCommandEncoder(protocolType),
		protocolType:   protocolType,
	}

	go t.reader()

	return t, nil
}

func (t *webTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.closeCh)

	defer func() {
		err := t.webTransport.Close()
		if err != nil {
			log.Println("error on web transport closing: ", err)
		}
	}()

	return t.stream.Close()
}

func (t *webTransport) reader() {
	defer func() {
		err := t.Close()
		if err != nil {
			fmt.Printf("error on stream close - [%s]\n", err)
		}
		err = t.webTransport.Close()
		if err != nil {
			fmt.Printf("error on wt client close - [%s]\n", err)
		}
	}()
	defer close(t.replyCh)

	var err error
	var replyDecoderData []byte
	r := bufio.NewReader(t.stream)
	for {
		if t.protocolType == protocol.TypeJSON {
			replyDecoderData, err = r.ReadBytes('\n')
			if err != nil {
				fmt.Printf("read error - [%s]\n", err)
				return
			}
			log.Println("<----", strings.Trim(string(replyDecoderData), "\n"))
		} else if t.protocolType == protocol.TypeProtobuf {
			msgLength, err := binary.ReadUvarint(r)
			if err != nil {
				if err == io.EOF {
					break
				}

				log.Fatalln("error on read message length: ", err)
			}

			msgLengthBytes := make([]byte, binary.MaxVarintLen64)
			bytesNum := binary.PutUvarint(msgLengthBytes, msgLength)
			data := make([]byte, msgLength)

			_, err = r.Read(data)
			if err != nil {
				if err == io.EOF {
					continue
				}

				log.Println("error on read bytes from stream buffer: ", err)
				return
			}

			replyDecoderData = append(msgLengthBytes[:bytesNum], data...)
		}

		decoder := newReplyDecoder(t.protocolType, replyDecoderData)
		reply, err := decoder.Decode()
		if err != nil {
			if err == io.EOF {
				continue
			}

			t.disconnect = &disconnect{Reason: "decode error", Reconnect: false}
			return
		}

		select {
		case <-t.closeCh:
			return
		case t.replyCh <- reply:
		default:
			// Can't keep up with server message rate.
			t.disconnect = &disconnect{Reason: "client slow", Reconnect: true}
			return
		}
	}
}

func (t *webTransport) Write(cmd *protocol.Command, timeout time.Duration) error {
	data, err := t.commandEncoder.Encode(cmd)
	if err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if timeout > 0 {
		_ = t.stream.SetWriteDeadline(time.Now().Add(timeout))
	}

	if t.protocolType == protocol.TypeJSON {
		_, err = t.stream.Write(append(data, '\n'))
	} else if t.protocolType == protocol.TypeProtobuf {
		_, err = t.stream.Write(data)
	}

	if timeout > 0 {
		_ = t.stream.SetWriteDeadline(time.Time{})
	}

	return err
}

func (t *webTransport) Read() (*protocol.Reply, *disconnect, error) {
	reply, ok := <-t.replyCh
	if !ok {
		return nil, t.disconnect, io.EOF
	}

	return reply, nil, nil
}
