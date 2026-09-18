package quic

import (
	"context"
	"errors"
	"sync"

	"github.com/holandyoung/quic-go/internal/protocol"
	"github.com/holandyoung/quic-go/internal/utils"
	"github.com/holandyoung/quic-go/internal/utils/ringbuffer"
	"github.com/holandyoung/quic-go/internal/wire"
)

var errDatagramsDisabled = errors.New("datagram support disabled")

const (
	maxDatagramSendQueueLen = 32
	maxDatagramRcvQueueLen  = 128
)

type datagramQueue struct {
	sendMx         sync.Mutex
	sendQueue      ringbuffer.RingBuffer[*wire.DatagramFrame]
	sent           chan struct{} // used to notify Add that a datagram was dequeued
	maxFrameSize   protocol.ByteCount
	sendErr        error
	sendGeneration uint64
	sendReset      chan struct{} // allocated only while sending replayable early data

	rcvMx    sync.Mutex
	rcvQueue [][]byte
	rcvd     chan struct{} // used to notify Receive that a new datagram was received

	closeErr error
	closed   chan struct{}

	hasData func()

	logger utils.Logger
}

func newDatagramQueue(hasData func(), logger utils.Logger) *datagramQueue {
	return &datagramQueue{
		hasData: hasData,
		rcvd:    make(chan struct{}, 1),
		sent:    make(chan struct{}, 1),
		closed:  make(chan struct{}),
		logger:  logger,
		sendErr: errDatagramsDisabled,
	}
}

// Add queues a new DATAGRAM frame for sending.
// Up to 32 DATAGRAM frames will be queued.
// Once that limit is reached, Add blocks until the queue size has reduced.
func (h *datagramQueue) Add(payload []byte, maxPayload protocol.ByteCount, version protocol.Version) error {
	h.sendMx.Lock()
	if h.sendErr != nil {
		err := h.sendErr
		h.sendMx.Unlock()
		return err
	}
	f := &wire.DatagramFrame{DataLenPresent: true}
	limit := min(f.MaxDataLen(h.maxFrameSize, version), maxPayload)
	if protocol.ByteCount(len(payload)) > limit || f.Length(version) > h.maxFrameSize {
		h.sendMx.Unlock()
		return &DatagramTooLargeError{MaxDatagramPayloadSize: int64(limit)}
	}
	generation := h.sendGeneration

	for {
		if generation != h.sendGeneration {
			h.sendMx.Unlock()
			return Err0RTTRejected
		}
		select {
		case <-h.closed:
			h.sendMx.Unlock()
			return h.closeErr
		default:
		}
		if h.sendQueue.Len() < maxDatagramSendQueueLen {
			f.Data = make([]byte, len(payload))
			copy(f.Data, payload)
			h.sendQueue.PushBack(f)
			h.sendMx.Unlock()
			h.hasData()
			return nil
		}
		select {
		case <-h.sent: // drain the queue so we don't loop immediately
		default:
		}
		reset := h.sendReset
		h.sendMx.Unlock()
		select {
		case <-h.closed:
			return h.closeErr
		case <-reset:
			return Err0RTTRejected
		case <-h.sent:
		}
		h.sendMx.Lock()
	}
}

// ApplyTransportParameters is called only when restored or handshake parameters
// become effective. After acceptance, limits can only increase. Rejection
// retires the old queue before a smaller or disabled policy can be applied.
func (h *datagramQueue) ApplyTransportParameters(maxFrameSize protocol.ByteCount, early bool) {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	h.maxFrameSize = maxFrameSize
	h.sendErr = nil
	if maxFrameSize <= 0 {
		h.sendErr = errDatagramsDisabled
	} else if early && h.sendReset == nil {
		h.sendReset = make(chan struct{})
	}
}

// Reject0RTT retires every queued early datagram and wakes all producers from
// that generation. Accepted new-generation sends cannot revive rejected data.
// The connection loop also owns Peek/Pop, so a peek cannot straddle rejection.
func (h *datagramQueue) Reject0RTT() {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	h.sendGeneration++
	h.maxFrameSize = 0
	h.sendErr = Err0RTTRejected
	h.sendQueue.Clear()
	if h.sendReset != nil {
		close(h.sendReset)
		h.sendReset = nil
	}
}

// Peek gets the next DATAGRAM frame for sending.
// If actually sent out, Pop needs to be called before the next call to Peek.
func (h *datagramQueue) Peek() *wire.DatagramFrame {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	if h.sendQueue.Empty() {
		return nil
	}
	return h.sendQueue.PeekFront()
}

func (h *datagramQueue) Pop() {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	_ = h.sendQueue.PopFront()
	select {
	case h.sent <- struct{}{}:
	default:
	}
}

// HandleDatagramFrame handles a received DATAGRAM frame.
func (h *datagramQueue) HandleDatagramFrame(f *wire.DatagramFrame) {
	data := make([]byte, len(f.Data))
	copy(data, f.Data)
	var queued bool
	h.rcvMx.Lock()
	if len(h.rcvQueue) < maxDatagramRcvQueueLen {
		h.rcvQueue = append(h.rcvQueue, data)
		queued = true
		select {
		case h.rcvd <- struct{}{}:
		default:
		}
	}
	h.rcvMx.Unlock()
	if !queued && h.logger.Debug() {
		h.logger.Debugf("Discarding received DATAGRAM frame (%d bytes payload)", len(f.Data))
	}
}

// Receive gets a received DATAGRAM frame.
func (h *datagramQueue) Receive(ctx context.Context) ([]byte, error) {
	for {
		h.rcvMx.Lock()
		if len(h.rcvQueue) > 0 {
			data := h.rcvQueue[0]
			h.rcvQueue = h.rcvQueue[1:]
			h.rcvMx.Unlock()
			return data, nil
		}
		h.rcvMx.Unlock()
		select {
		case <-h.rcvd:
			continue
		case <-h.closed:
			return nil, h.closeErr
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (h *datagramQueue) CloseWithError(e error) {
	h.closeErr = e
	close(h.closed)
}
