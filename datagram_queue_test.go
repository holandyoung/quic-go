package quic

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatagramQueuePeekAndPop(t *testing.T) {
	var queued []struct{}
	queue := newTestDatagramQueue(func() { queued = append(queued, struct{}{}) }, utils.DefaultLogger)
	require.Nil(t, queue.Peek())
	require.Empty(t, queued)
	require.NoError(t, queue.Add([]byte("foo"), protocol.MaxByteCount, protocol.Version1))
	require.Len(t, queued, 1)
	require.Equal(t, &wire.DatagramFrame{DataLenPresent: true, Data: []byte("foo")}, queue.Peek())
	// calling peek again returns the same datagram
	require.Equal(t, &wire.DatagramFrame{DataLenPresent: true, Data: []byte("foo")}, queue.Peek())
	queue.Pop()
	require.Nil(t, queue.Peek())
}

func TestDatagramQueueSendQueueLength(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue := newTestDatagramQueue(func() {}, utils.DefaultLogger)

		for range maxDatagramSendQueueLen {
			require.NoError(t, queue.Add([]byte{0}, protocol.MaxByteCount, protocol.Version1))
		}
		errChan := make(chan error, 1)
		go func() { errChan <- queue.Add([]byte("foobar"), protocol.MaxByteCount, protocol.Version1) }()

		synctest.Wait()

		select {
		case <-errChan:
			t.Fatal("expected to not receive error")
		default:
		}

		// peeking doesn't remove the datagram from the queue...
		require.NotNil(t, queue.Peek())
		synctest.Wait()
		select {
		case <-errChan:
			t.Fatal("expected to not receive error")
		default:
		}

		// ...but popping does
		queue.Pop()
		synctest.Wait()
		select {
		case err := <-errChan:
			require.NoError(t, err)
		default:
			t.Fatal("timeout")
		}
		// pop all the remaining datagrams
		for range maxDatagramSendQueueLen - 1 {
			queue.Pop()
		}
		f := queue.Peek()
		require.NotNil(t, f)
		require.Equal(t, &wire.DatagramFrame{DataLenPresent: true, Data: []byte("foobar")}, f)
	})
}

func TestDatagramQueueReceive(t *testing.T) {
	queue := newTestDatagramQueue(func() {}, utils.DefaultLogger)

	// receive frames that were received earlier
	queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte("foo")})
	queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte("bar")})
	data, err := queue.Receive(context.Background())
	require.NoError(t, err)
	require.Equal(t, []byte("foo"), data)
	data, err = queue.Receive(context.Background())
	require.NoError(t, err)
	require.Equal(t, []byte("bar"), data)
}

func TestDatagramQueueReceiveBlocking(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue := newTestDatagramQueue(func() {}, utils.DefaultLogger)

		// block until a new frame is received
		type result struct {
			data []byte
			err  error
		}
		resultChan := make(chan result, 1)
		go func() {
			data, err := queue.Receive(context.Background())
			resultChan <- result{data, err}
		}()

		synctest.Wait()

		select {
		case <-resultChan:
			t.Fatal("expected to not receive result")
		default:
		}
		queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte("foobar")})
		synctest.Wait()
		select {
		case result := <-resultChan:
			require.NoError(t, result.err)
			require.Equal(t, []byte("foobar"), result.data)
		default:
			t.Fatal("should have received a datagram frame")
		}

		// unblock when the context is canceled
		ctx, cancel := context.WithCancel(context.Background())
		errChan := make(chan error, 1)
		go func() {
			_, err := queue.Receive(ctx)
			errChan <- err
		}()

		synctest.Wait()
		select {
		case <-errChan:
			t.Fatal("expected to not receive error")
		default:
		}

		cancel()
		synctest.Wait()

		select {
		case err := <-errChan:
			require.ErrorIs(t, err, context.Canceled)
		default:
			t.Fatal("should have received a context canceled error")
		}
	})
}

func TestDatagramQueueClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue := newTestDatagramQueue(func() {}, utils.DefaultLogger)

		for range maxDatagramSendQueueLen {
			require.NoError(t, queue.Add([]byte{0}, protocol.MaxByteCount, protocol.Version1))
		}
		errChan1 := make(chan error, 1)
		go func() { errChan1 <- queue.Add([]byte("foobar"), protocol.MaxByteCount, protocol.Version1) }()
		errChan2 := make(chan error, 1)
		go func() {
			_, err := queue.Receive(context.Background())
			errChan2 <- err
		}()

		queue.CloseWithError(assert.AnError)
		synctest.Wait()

		select {
		case err := <-errChan1:
			require.ErrorIs(t, err, assert.AnError)
		default:
			t.Fatal("should have received an error")
		}

		select {
		case err := <-errChan2:
			require.ErrorIs(t, err, assert.AnError)
		default:
			t.Fatal("should have received an error")
		}
	})
}

func TestDatagramQueueEffectiveLimits(t *testing.T) {
	queue := newDatagramQueue(func() {}, utils.DefaultLogger)
	require.ErrorIs(t, queue.Add(nil, 100, protocol.Version1), errDatagramsDisabled)
	// A frame with a length field needs two bytes even for an empty payload.
	queue.ApplyTransportParameters(1, false)
	var tooLarge *DatagramTooLargeError
	require.ErrorAs(t, queue.Add(nil, 100, protocol.Version1), &tooLarge)
	queue.ApplyTransportParameters(2, false)
	require.NoError(t, queue.Add(nil, 100, protocol.Version1))
	require.EqualValues(t, 2, queue.Peek().Length(protocol.Version1))
	queue.Pop()
	queue.ApplyTransportParameters(100, false)
	require.ErrorAs(t, queue.Add([]byte("large"), 4, protocol.Version1), &tooLarge)
	require.EqualValues(t, 4, tooLarge.MaxDatagramPayloadSize)
	require.NoError(t, queue.Add([]byte("fits"), 4, protocol.Version1))
}

func newTestDatagramQueue(hasData func(), logger utils.Logger) *datagramQueue {
	q := newDatagramQueue(hasData, logger)
	q.ApplyTransportParameters(16383, false)
	return q
}
