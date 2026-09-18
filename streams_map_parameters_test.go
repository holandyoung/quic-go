package quic

import (
	"context"
	"sync"
	"testing"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func parameterTestConnection(t *testing.T) *Conn {
	t.Helper()
	sender := NewMockStreamSender(gomock.NewController(t))
	sender.EXPECT().onHasStreamData(gomock.Any(), gomock.Any()).AnyTimes()
	sender.EXPECT().onHasStreamControlFrame(gomock.Any(), gomock.Any()).AnyTimes()
	c := &Conn{
		config:             &Config{InitialStreamReceiveWindow: 4096, MaxStreamReceiveWindow: 4096},
		connFlowController: newTestStreamFlowController(0).connection,
		rttStats:           utils.NewRTTStats(), logger: utils.DefaultLogger,
	}
	c.streamsMap = newStreamsMap(context.Background(), sender, func(wire.Frame) {}, c.newFlowController, 4, 4, protocol.PerspectiveClient)
	return c
}

func TestStreamParametersRemainEffectiveUntilApplied(t *testing.T) {
	c := parameterTestConnection(t)
	old := &wire.TransportParameters{MaxBidiStreamNum: 10, MaxUniStreamNum: 10, InitialMaxStreamDataBidiRemote: 10, InitialMaxStreamDataBidiLocal: 20, InitialMaxStreamDataUni: 30}
	c.peerParams.Store(old)
	c.streamsMap.HandleTransportParameters(old)
	pending := *old
	pending.InitialMaxStreamDataBidiRemote = 100
	pending.InitialMaxStreamDataBidiLocal = 200
	pending.InitialMaxStreamDataUni = 300
	pending.EnableResetStreamAt = true
	// Receiving authenticated parameters does not make them legal for 0-RTT.
	c.peerParams.Store(&pending)
	bidi, err := c.streamsMap.OpenStream()
	require.NoError(t, err)
	uni, err := c.streamsMap.OpenUniStream()
	require.NoError(t, err)
	incoming, err := c.streamsMap.incomingBidiStreams.GetOrOpenStream(1)
	require.NoError(t, err)
	accepted, err := c.streamsMap.AcceptStream(context.Background())
	require.NoError(t, err)
	require.Same(t, incoming, accepted)
	unaccepted, err := c.streamsMap.incomingBidiStreams.GetOrOpenStream(5)
	require.NoError(t, err)
	require.Equal(t, protocol.ByteCount(10), bidi.sendStr.flowController.SendWindowSize())
	require.Equal(t, protocol.ByteCount(30), uni.flowController.SendWindowSize())
	require.Equal(t, protocol.ByteCount(20), incoming.sendStr.flowController.SendWindowSize())
	// A separately received MAX_STREAM_DATA must not be overwritten by the
	// initial transport window when the handshake finally applies parameters.
	unaccepted.updateSendWindow(500)
	c.streamsMap.HandleTransportParameters(&pending)
	require.Equal(t, protocol.ByteCount(100), bidi.sendStr.flowController.SendWindowSize())
	require.Equal(t, protocol.ByteCount(300), uni.flowController.SendWindowSize())
	require.Equal(t, protocol.ByteCount(200), incoming.sendStr.flowController.SendWindowSize())
	require.Equal(t, protocol.ByteCount(500), unaccepted.sendStr.flowController.SendWindowSize())
	require.True(t, supportsResetStreamAt(t, bidi))
	require.True(t, supportsResetStreamAt(t, incoming))
	require.True(t, supportsResetStreamAt(t, unaccepted))
	require.True(t, sendStreamSupportsResetStreamAt(t, uni))
	future, err := c.streamsMap.OpenStream()
	require.NoError(t, err)
	require.Equal(t, protocol.ByteCount(100), future.sendStr.flowController.SendWindowSize())
	require.True(t, supportsResetStreamAt(t, future))
}

func TestStreamParametersConcurrentPublicationAndCreation(t *testing.T) {
	c := parameterTestConnection(t)
	initial := &wire.TransportParameters{MaxBidiStreamNum: 1024, MaxUniStreamNum: 1024, InitialMaxStreamDataBidiRemote: 1, InitialMaxStreamDataUni: 1}
	c.peerParams.Store(initial)
	c.streamsMap.HandleTransportParameters(initial)
	var wg sync.WaitGroup
	start := make(chan struct{})
	bidiStreams := make(chan *Stream, 512)
	uniStreams := make(chan *SendStream, 512)
	for range 4 {
		wg.Go(func() {
			<-start
			for range 128 {
				bidi, err := c.streamsMap.OpenStreamSync(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				uni, err := c.streamsMap.OpenUniStream()
				if err != nil {
					t.Error(err)
					return
				}
				bidiStreams <- bidi
				uniStreams <- uni
			}
		})
	}
	wg.Go(func() {
		<-start
		for i := protocol.ByteCount(2); i <= 128; i++ {
			parameters := *initial
			parameters.InitialMaxStreamDataBidiRemote = i
			parameters.InitialMaxStreamDataUni = i * 2
			parameters.EnableResetStreamAt = true
			c.peerParams.Store(&parameters)
			c.streamsMap.HandleTransportParameters(&parameters)
		}
	})
	close(start)
	wg.Wait()
	close(bidiStreams)
	close(uniStreams)
	for s := range bidiStreams {
		require.Equal(t, protocol.ByteCount(128), s.sendStr.flowController.SendWindowSize())
		require.True(t, supportsResetStreamAt(t, s))
	}
	for s := range uniStreams {
		require.Equal(t, protocol.ByteCount(256), s.flowController.SendWindowSize())
		require.True(t, sendStreamSupportsResetStreamAt(t, s))
	}
}
