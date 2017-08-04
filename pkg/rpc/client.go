package rpc

import (
	"fmt"
	"github.com/Azure/draft/pkg/version"
	"github.com/golang/protobuf/ptypes/empty"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"io"
)

type clientImpl struct {
	opts clientOpts
}

func newClientImpl(opts ...ClientOpt) Client {
	var c clientImpl
	for _, opt := range opts {
		opt(&c.opts)
	}
	return &c
}

// Version implementes rpc.Client.Version
func (c *clientImpl) Version(ctx context.Context) (*version.Version, error) {
	conn, err := connect(c)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := NewDraftClient(conn)
	rpcctx := context.Background()

	r, err := client.GetVersion(rpcctx, &empty.Empty{})
	if err != nil {
		return nil, fmt.Errorf("error getting server version: %v", err)
	}
	v := &version.Version{SemVer: r.SemVer, GitCommit: r.GitCommit, GitTreeState: r.GitTreeState}
	return v, nil
}

// UpBuild implementes rpc.Client.UpBuild
func (c *clientImpl) UpBuild(ctx context.Context, req *UpRequest, outc chan<- *UpSummary) (err error) {
	conn, err := connect(c)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := NewDraftClient(conn)
	rpcctx := context.Background()

	msgc := make(chan *UpMessage, 2)
	errc := make(chan error)
	go func() {
		if err := up_build(rpcctx, client, req, msgc); err != nil {
			errc <- err
		}
		close(errc)
	}()
	defer close(outc)
	for msgc != nil || errc != nil {
		select {
		case msg, ok := <-msgc:
			if !ok {
				msgc = nil
				continue
			}
			outc <- msg.GetUpSummary()
		case err, ok := <-errc:
			if !ok {
				errc = nil
				continue
			}
			return err
		}
	}
	return nil
}

// UpStream implementes rpc.Client.UpStream
func (c *clientImpl) UpStream(apictx context.Context, reqc <-chan *UpRequest, outc chan<- *UpSummary) error {
	conn, err := connect(c)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := NewDraftClient(conn)
	rpcctx := context.Background()

	msgc := make(chan *UpMessage)
	go func() {
		for msg := range msgc {
			if summary := msg.GetUpSummary(); summary != nil {
				outc <- summary
			}
		}
		close(outc)
	}()
	return up_stream(rpcctx, client, reqc, msgc)
}

func up_build(ctx context.Context, rpc DraftClient, msg *UpRequest, outc chan<- *UpMessage) error {
	s, err := rpc.UpBuild(ctx, &UpMessage{&UpMessage_UpRequest{msg}})
	if err != nil {
		return fmt.Errorf("rpc error handling up_build: %v", err)
	}
	defer close(outc)
	for {
		resp, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("rpc error handling up_build recv: %v", err)
		}
		outc <- resp
	}
}

func up_stream(ctx context.Context, rpc DraftClient, send <-chan *UpRequest, recv chan<- *UpMessage) error {
	stream, err := rpc.UpStream(ctx)
	if err != nil {
		return fmt.Errorf("rpc error handling up_stream: %v", err)
	}
	done := make(chan struct{})
	errc := make(chan error)
	defer func() {
		stream.CloseSend()
		<-done
		close(recv)
		close(errc)
	}()
	go func() {
		for {
			m, err := stream.Recv()
			if err == io.EOF {
				close(done)
				return
			}
			if err != nil {
				errc <- fmt.Errorf("failed to receive a summary: %v", err)
				return
			}
			recv <- m
		}
	}()
	for {
		select {
		case msg, ok := <-send:
			if !ok {
				return nil
			}
			req := &UpMessage{&UpMessage_UpRequest{msg}}
			if err := stream.Send(req); err != nil {
				return fmt.Errorf("failed to send an up message: %v", err)
			}
		case err := <-errc:
			return err
		}
	}
}

// connect connects the DraftClient to the DraftServer.
func connect(c *clientImpl, opts ...grpc.DialOption) (conn *grpc.ClientConn, err error) {
	if conn, err = grpc.Dial(c.opts.addr, grpc.WithInsecure()); err != nil {
		return nil, fmt.Errorf("failed to dial %q: %v", c.opts.addr, err)
	}
	return conn, nil
}
