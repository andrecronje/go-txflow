package mempool

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io/ioutil"
	mrand "math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	amino "github.com/tendermint/go-amino"

	"github.com/andrecronje/babble-abci/types"
	"github.com/tendermint/tendermint/abci/example/counter"
	"github.com/tendermint/tendermint/abci/example/kvstore"
	abciserver "github.com/tendermint/tendermint/abci/server"
	abci "github.com/tendermint/tendermint/abci/types"
	cfg "github.com/tendermint/tendermint/config"
	cmn "github.com/tendermint/tendermint/libs/common"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/proxy"
)

// A cleanupFunc cleans up any config / test files created for a particular
// test.
type cleanupFunc func()

func newEventpoolWithApp(cc proxy.ClientCreator) (*Eventpool, cleanupFunc) {
	return newEventpoolWithAppAndConfig(cc, cfg.ResetTestRoot("eventpool_test"))
}

func newEventpoolWithAppAndConfig(cc proxy.ClientCreator, config *cfg.Config) (*Eventpool, cleanupFunc) {
	appConnMem, _ := cc.NewABCIClient()
	appConnMem.SetLogger(log.TestingLogger().With("module", "abci-client", "connection", "eventpool"))
	err := appConnMem.Start()
	if err != nil {
		panic(err)
	}
	eventpool := NewEventpool(config.Mempool, appConnMem, 0)
	eventpool.SetLogger(log.TestingLogger())
	return eventpool, func() { os.RemoveAll(config.RootDir) }
}

func ensureNoFire(t *testing.T, ch <-chan struct{}, timeoutMS int) {
	timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	select {
	case <-ch:
		t.Fatal("Expected not to fire")
	case <-timer.C:
	}
}

func ensureFire(t *testing.T, ch <-chan struct{}, timeoutMS int) {
	timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	select {
	case <-ch:
	case <-timer.C:
		t.Fatal("Expected to fire")
	}
}

func checkEvents(t *testing.T, eventpool *Eventpool, count int, peerID uint16) []types.Event {
	events := make([]types.Event, count)
	eventInfo := EventInfo{PeerID: peerID}
	for i := 0; i < count; i++ {
		eventBytes := make([]byte, 20)
		events[i] = eventBytes
		_, err := rand.Read(eventBytes)
		if err != nil {
			t.Error(err)
		}
		if err := eventpool.CheckEventWithInfo(eventBytes, nil, eventInfo); err != nil {
			// Skip invalid events.
			// TestEventpoolFilters will fail otherwise. It asserts a number of events
			// returned.
			if IsPreCheckError(err) {
				continue
			}
			t.Fatalf("CheckEvent failed: %v while checking #%d tx", err, i)
		}
	}
	return events
}

func TestEventpoolUpdateAddsEventsToCache(t *testing.T) {
	app := kvstore.NewKVStoreApplication()
	cc := proxy.NewLocalClientCreator(app)
	eventpool, cleanup := newEventpoolWithApp(cc)
	defer cleanup()
	eventpool.Update(1, []types.Event{[]byte{0x01}}, nil, nil)
	err := eventpool.CheckEvent([]byte{0x01}, nil)
	if assert.Error(t, err) {
		assert.Equal(t, ErrEventInCache, err)
	}
}

func TestEventsAvailable(t *testing.T) {
	app := kvstore.NewKVStoreApplication()
	cc := proxy.NewLocalClientCreator(app)
	eventpool, cleanup := newEventpoolWithApp(cc)
	defer cleanup()
	eventpool.EnableEventsAvailable()

	timeoutMS := 500

	// with no events, it shouldnt fire
	ensureNoFire(t, eventpool.EventsAvailable(), timeoutMS)

	// send a bunch of events, it should only fire once
	events := checkEvents(t, eventpool, 100, UnknownPeerID)
	ensureFire(t, eventpool.EventsAvailable(), timeoutMS)
	ensureNoFire(t, eventpool.EventsAvailable(), timeoutMS)

	// call update with half the events.
	// it should fire once now for the new height
	// since there are still events left
	committedEvents, events := events[:50], events[50:]
	if err := eventpool.Update(1, committedEvents, nil, nil); err != nil {
		t.Error(err)
	}
	ensureFire(t, eventpool.EventsAvailable(), timeoutMS)
	ensureNoFire(t, eventpool.EventsAvailable(), timeoutMS)

	// send a bunch more events. we already fired for this height so it shouldnt fire again
	moreEvents := checkEvents(t, eventpool, 50, UnknownPeerID)
	ensureNoFire(t, eventpool.EventsAvailable(), timeoutMS)

	// now call update with all the events. it should not fire as there are no events left
	committedEvents = append(events, moreEvents...)
	if err := eventpool.Update(2, committedEvents, nil, nil); err != nil {
		t.Error(err)
	}
	ensureNoFire(t, eventpool.EventsAvailable(), timeoutMS)

	// send a bunch more events, it should only fire once
	checkEvents(t, eventpool, 100, UnknownPeerID)
	ensureFire(t, eventpool.EventsAvailable(), timeoutMS)
	ensureNoFire(t, eventpool.EventsAvailable(), timeoutMS)
}

func TestSerialReap(t *testing.T) {
	app := counter.NewCounterApplication(true)
	app.SetOption(abci.RequestSetOption{Key: "serial", Value: "on"})
	cc := proxy.NewLocalClientCreator(app)

	mempool, cleanup := newEventpoolWithApp(cc)
	defer cleanup()

	appConnCon, _ := cc.NewABCIClient()
	appConnCon.SetLogger(log.TestingLogger().With("module", "abci-client", "connection", "consensus"))
	err := appConnCon.Start()
	require.Nil(t, err)

	cacheMap := make(map[string]struct{})
	deliverEventsRange := func(start, end int) {
		// Deliver some events.
		for i := start; i < end; i++ {

			// This will succeed
			eventBytes := make([]byte, 8)
			binary.BigEndian.PutUint64(eventBytes, uint64(i))
			err := eventpool.CheckEvents(eventBytes, nil)
			_, cached := cacheMap[string(eventBytes)]
			if cached {
				require.NotNil(t, err, "expected error for cached event")
			} else {
				require.Nil(t, err, "expected no err for uncached event")
			}
			cacheMap[string(eventBytes)] = struct{}{}

			// Duplicates are cached and should return error
			err = eventpool.CheckEvent(eventBytes, nil)
			require.NotNil(t, err, "Expected error after CheckEvent on duplicated event")
		}
	}

	updateRange := func(start, end int) {
		events := make([]types.Event, 0)
		for i := start; i < end; i++ {
			eventBytes := make([]byte, 8)
			binary.BigEndian.PutUint64(eventBytes, uint64(i))
			events = append(events, eventBytes)
		}
		if err := eventpool.Update(0, events, nil, nil); err != nil {
			t.Error(err)
		}
	}

	commitRange := func(start, end int) {
		// Deliver some events.
		for i := start; i < end; i++ {
			eventBytes := make([]byte, 8)
			binary.BigEndian.PutUint64(eventBytes, uint64(i))
			res, err := appConnCon.DeliverEventSync(eventBytes)
			if err != nil {
				t.Errorf("Client error committing event: %v", err)
			}
			if res.IsErr() {
				t.Errorf("Error committing event. Code:%v result:%X log:%v",
					res.Code, res.Data, res.Log)
			}
		}
		res, err := appConnCon.CommitSync()
		if err != nil {
			t.Errorf("Client error committing: %v", err)
		}
		if len(res.Data) != 8 {
			t.Errorf("Error committing. Hash:%X", res.Data)
		}
	}

	//----------------------------------------

	// Deliver some events.
	delivereventsRange(0, 100)

	// Reap the events.
	reapCheck(100)

	// Reap again.  We should get the same amount
	reapCheck(100)

	// Deliver 0 to 999, we should reap 900 new events
	// because 100 were already counted.
	deliverTxsRange(0, 1000)

	// Reap the events.
	reapCheck(1000)

	// Reap again.  We should get the same amount
	reapCheck(1000)

	// Commit from the conensus AppConn
	commitRange(0, 500)
	updateRange(0, 500)

	// We should have 500 left.
	reapCheck(500)

	// Deliver 100 invalid events and 100 valid events
	deliverEventsRange(900, 1100)

	// We should have 600 now.
	reapCheck(600)
}

func TestEventpoolCloseWAL(t *testing.T) {
	// 1. Create the temporary directory for eventpool and WAL testing.
	rootDir, err := ioutil.TempDir("", "eventpool-test")
	require.Nil(t, err, "expecting successful tmpdir creation")
	defer os.RemoveAll(rootDir)

	// 2. Ensure that it doesn't contain any elements -- Sanity check
	m1, err := filepath.Glob(filepath.Join(rootDir, "*"))
	require.Nil(t, err, "successful globbing expected")
	require.Equal(t, 0, len(m1), "no matches yet")

	// 3. Create the eventpool
	wcfg := cfg.DefaultEventpoolConfig()
	wcfg.RootDir = rootDir
	defer os.RemoveAll(wcfg.RootDir)
	app := kvstore.NewKVStoreApplication()
	cc := proxy.NewLocalClientCreator(app)
	appConnMem, _ := cc.NewABCIClient()
	eventpool := NewEventpool(wcfg, appConnMem, 10)
	eventpool.InitWAL()

	// 4. Ensure that the directory contains the WAL file
	m2, err := filepath.Glob(filepath.Join(rootDir, "*"))
	require.Nil(t, err, "successful globbing expected")
	require.Equal(t, 1, len(m2), "expecting the wal match in")

	// 5. Write some contents to the WAL
	eventpool.CheckEvent(types.Event([]byte("foo")), nil)
	walFilepath := eventpool.wal.Path
	sum1 := checksumFile(walFilepath, t)

	// 6. Sanity check to ensure that the written event matches the expectation.
	require.Equal(t, sum1, checksumIt([]byte("foo\n")), "foo with a newline should be written")

	// 7. Invoke CloseWAL() and ensure it discards the
	// WAL thus any other write won't go through.
	eventpool.CloseWAL()
	eventpool.CheckEvent(types.Event([]byte("bar")), nil)
	sum2 := checksumFile(walFilepath, t)
	require.Equal(t, sum1, sum2, "expected no change to the WAL after invoking CloseWAL() since it was discarded")

	// 8. Sanity check to ensure that the WAL file still exists
	m3, err := filepath.Glob(filepath.Join(rootDir, "*"))
	require.Nil(t, err, "successful globbing expected")
	require.Equal(t, 1, len(m3), "expecting the wal match in")
}

// Size of the amino encoded EventMessage is the length of the
// encoded byte array, plus 1 for the struct field, plus 4
// for the amino prefix.
func eventMessageSize(event types.Event) int {
	return amino.ByteSliceSize(event) + 1 + 4
}

func TestEventpoolMaxMsgSize(t *testing.T) {
	app := kvstore.NewKVStoreApplication()
	cc := proxy.NewLocalClientCreator(app)
	eventpool, cleanup := newEventpoolWithApp(cc)
	defer cleanup()

	testCases := []struct {
		len int
		err bool
	}{
		// check small events. no error
		{10, false},
		{1000, false},
		{1000000, false},

		// check around maxEventSize
		// changes from no error to error
		{maxEventSize - 2, false},
		{maxEventSize - 1, false},
		{maxEventSize, false},
		{maxEventSize + 1, true},
		{maxEventSize + 2, true},

		// check around maxMsgSize. all error
		{maxMsgSize - 1, true},
		{maxMsgSize, true},
		{maxMsgSize + 1, true},
	}

	for i, testCase := range testCases {
		caseString := fmt.Sprintf("case %d, len %d", i, testCase.len)

		event := cmn.RandBytes(testCase.len)
		err := mempl.CheckEvent(event, nil)
		msg := &EventMessage{event}
		encoded := cdc.MustMarshalBinaryBare(msg)
		require.Equal(t, len(encoded), eventMessageSize(event), caseString)
		if !testCase.err {
			require.True(t, len(encoded) <= maxMsgSize, caseString)
			require.NoError(t, err, caseString)
		} else {
			require.True(t, len(encoded) > maxMsgSize, caseString)
			require.Equal(t, err, ErrEventTooLarge, caseString)
		}
	}

}

func TestEventpoolEventsBytes(t *testing.T) {
	app := kvstore.NewKVStoreApplication()
	cc := proxy.NewLocalClientCreator(app)
	config := cfg.ResetTestRoot("mempool_test")
	config.Mempool.MaxTxsBytes = 10
	eventpool, cleanup := newEventpoolWithAppAndConfig(cc, config)
	defer cleanup()

	// 1. zero by default
	assert.EqualValues(t, 0, eventpool.EventsBytes())

	// 2. len(event) after CheckEvent
	err := eventpool.CheckEvent([]byte{0x01}, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 1, eventpool.EventsBytes())

	// 3. zero again after event is removed by Update
	eventpool.Update(1, []types.Event{[]byte{0x01}}, nil, nil)
	assert.EqualValues(t, 0, eventpool.EventsBytes())

	// 4. zero after Flush
	err = eventpool.CheckEvent([]byte{0x02, 0x03}, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 2, eventpool.EventsBytes())

	eventpool.Flush()
	assert.EqualValues(t, 0, eventpool.EventsBytes())

	// 5. ErrEventpoolIsFull is returned when/if MaxEventsBytes limit is reached.
	err = eventpool.CheckEvent([]byte{0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04}, nil)
	require.NoError(t, err)
	err = eventpool.CheckEvent([]byte{0x05}, nil)
	if assert.Error(t, err) {
		assert.IsType(t, ErrEventpoolIsFull{}, err)
	}

	// 6. zero after event is rechecked and removed due to not being valid anymore
	app2 := counter.NewCounterApplication(true)
	cc = proxy.NewLocalClientCreator(app2)
	eventpool, cleanup = newEventpoolWithApp(cc)
	defer cleanup()

	eventBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(eventBytes, uint64(0))

	err = eventpool.CheckEvent(eventBytes, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 8, eventpool.EventsBytes())

	appConnCon, _ := cc.NewABCIClient()
	appConnCon.SetLogger(log.TestingLogger().With("module", "abci-client", "connection", "consensus"))
	err = appConnCon.Start()
	require.Nil(t, err)
	defer appConnCon.Stop()
	res, err := appConnCon.DeliverTxSync(txBytes)
	require.NoError(t, err)
	require.EqualValues(t, 0, res.Code)
	res2, err := appConnCon.CommitSync()
	require.NoError(t, err)
	require.NotEmpty(t, res2.Data)

	// Pretend like we committed nothing so eventBytes gets rechecked and removed.
	eventpool.Update(1, []types.Event{}, nil, nil)
	assert.EqualValues(t, 0, eventpool.EventsBytes())
}

// This will non-deterministically catch some concurrency failures like
// https://github.com/tendermint/tendermint/issues/3509
// TODO: all of the tests should probably also run using the remote proxy app
// since otherwise we're not actually testing the concurrency of the eventpool here!
func TestEventpoolRemoteAppConcurrency(t *testing.T) {
	sockPath := fmt.Sprintf("unix:///tmp/echo_%v.sock", cmn.RandStr(6))
	app := kvstore.NewKVStoreApplication()
	cc, server := newRemoteApp(t, sockPath, app)
	defer server.Stop()
	config := cfg.ResetTestRoot("eventpool_test")
	eventpool, cleanup := newEventpoolWithAppAndConfig(cc, config)
	defer cleanup()

	// generate small number of txs
	nEvents := 10
	eventLen := 200
	events := make([]types.Event, nEvents)
	for i := 0; i < nEvents; i++ {
		events[i] = cmn.RandBytes(eventLen)
	}

	// simulate a group of peers sending them over and over
	N := config.Mempool.Size
	maxPeers := 5
	for i := 0; i < N; i++ {
		peerID := mrand.Intn(maxPeers)
		eventNum := mrand.Intn(nEvents)
		event := events[int(eventNum)]

		// this will err with ErrEventInCache many times ...
		eventpool.CheckEventWithInfo(event, nil, EventInfo{PeerID: uint16(peerID)})
	}
	err := eventpool.FlushAppConn()
	require.NoError(t, err)
}

// caller must close server
func newRemoteApp(t *testing.T, addr string, app abci.Application) (clientCreator proxy.ClientCreator, server cmn.Service) {
	clientCreator = proxy.NewRemoteClientCreator(addr, "socket", true)

	// Start server
	server = abciserver.NewSocketServer(addr, app)
	server.SetLogger(log.TestingLogger().With("module", "abci-server"))
	if err := server.Start(); err != nil {
		t.Fatalf("Error starting socket server: %v", err.Error())
	}
	return clientCreator, server
}
func checksumIt(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func checksumFile(p string, t *testing.T) string {
	data, err := ioutil.ReadFile(p)
	require.Nil(t, err, "expecting successful read of %q", p)
	return checksumIt(data)
}
