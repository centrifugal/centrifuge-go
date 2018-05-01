package centrifuge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge-go/internal/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

func extractDisconnectGRPC(md metadata.MD) *disconnect {
	if value, ok := md["disconnect"]; ok {
		if len(value) > 0 {
			d := value[0]
			var dis disconnect
			err := json.Unmarshal([]byte(d), &dis)
			if err == nil {
				return &dis
			}
		}
	}
	return nil
}

type grpcTransport struct {
	mu         sync.Mutex
	conn       *grpc.ClientConn
	config     GRPCConfig
	disconnect *disconnect
	replyCh    chan *proto.Reply
	stream     proto.Centrifuge_CommunicateClient
	closed     bool
	closeCh    chan struct{}
}

// GRPCConfig configures GRPC transport.
type GRPCConfig struct {
	TLS      bool
	CertFile string
}

func newGRPCTransport(u string, config GRPCConfig) (transport, error) {
	var opts []grpc.DialOption
	if config.TLS && config.CertFile != "" {
		// openssl req -x509 -nodes -days 365 -newkey rsa:2048 -keyout ./server.key -out ./server.cert
		creds, err := credentials.NewClientTLSFromFile(config.CertFile, "")
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(creds))
	} else if config.TLS {
		creds := credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})
		opts = append(opts, grpc.WithTransportCredentials(creds))
	} else {
		opts = append(opts, grpc.WithInsecure())
	}

	urlObject, err := url.Parse(u)
	if err != nil {
		return nil, err
	}

	conn, err := grpc.Dial(urlObject.Host, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %s", err)
	}

	client := proto.NewCentrifugeClient(conn)

	stream, err := client.Communicate(context.Background())
	if err != nil {
		conn.Close()
		return nil, err
	}

	t := &grpcTransport{
		conn:    conn,
		stream:  stream,
		config:  config,
		replyCh: make(chan *proto.Reply, 128),
		closeCh: make(chan struct{}),
	}

	go t.reader()
	return t, nil
}

// DoWithTimeout runs f and returns its error.  If the deadline d elapses first,
// it returns a grpc DeadlineExceeded error instead.
func DoWithTimeout(f func() error, d time.Duration) error {
	errChan := make(chan error, 1)
	go func() {
		errChan <- f()
		close(errChan)
	}()
	t := time.NewTimer(d)
	select {
	case <-t.C:
		return fmt.Errorf("write timeout")
	case err := <-errChan:
		if !t.Stop() {
			<-t.C
		}
		return err
	}
}

func (t *grpcTransport) Write(cmd *proto.Command, timeout time.Duration) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := DoWithTimeout(func() error {
		return t.stream.Send(cmd)
	}, timeout); err != nil {
		return err
	}
	return nil
}

func (t *grpcTransport) Read() (*proto.Reply, *disconnect, error) {
	select {
	case reply, ok := <-t.replyCh:
		if !ok {
			return nil, t.disconnect, io.EOF
		}
		return reply, nil, nil
	}
}

func (t *grpcTransport) reader() {
	defer t.Close()
	defer close(t.replyCh)

	for {
		reply, err := t.stream.Recv()
		if err != nil {
			disconnect := extractDisconnectGRPC(t.stream.Trailer())
			t.disconnect = disconnect
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

func (t *grpcTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.closeCh)
	return t.conn.Close()
}
