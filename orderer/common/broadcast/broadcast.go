/*
Copyright IBM Corp. 2016 All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

                 http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package broadcast

import (
	"github.com/hyperledger/fabric/orderer/common/broadcastfilter"
	"github.com/hyperledger/fabric/orderer/common/configtx"
	cb "github.com/hyperledger/fabric/protos/common"
	ab "github.com/hyperledger/fabric/protos/orderer"

	"github.com/op/go-logging"
)

var logger = logging.MustGetLogger("orderer/common/broadcast")

func init() {
	logging.SetLevel(logging.DEBUG, "")
}

// Target defines an interface which the broadcast handler will direct broadcasts to
type Target interface {
	// Enqueue accepts a message and returns true on acceptance, or false on shutdown
	Enqueue(env *cb.Envelope) bool
}

// Handler defines an interface which handles broadcasts
type Handler interface {
	// Handle starts a service thread for a given gRPC connection and services the broadcast connection
	Handle(srv ab.AtomicBroadcast_BroadcastServer) error
}

type handlerImpl struct {
	queueSize     int
	target        Target
	filters       *broadcastfilter.RuleSet
	configManager configtx.Manager
	exitChan      chan struct{}
}

// NewHandlerImpl constructs a new implementation of the Handler interface
func NewHandlerImpl(queueSize int, target Target, filters *broadcastfilter.RuleSet, configManager configtx.Manager) Handler {
	return &handlerImpl{
		queueSize:     queueSize,
		filters:       filters,
		configManager: configManager,
		target:        target,
		exitChan:      make(chan struct{}),
	}
}

// Handle starts a service thread for a given gRPC connection and services the broadcast connection
func (bh *handlerImpl) Handle(srv ab.AtomicBroadcast_BroadcastServer) error {
	b := newBroadcaster(bh)
	defer close(b.queue)
	go b.drainQueue()
	return b.queueEnvelopes(srv)
}

type broadcaster struct {
	bs    *handlerImpl
	queue chan *cb.Envelope
}

func newBroadcaster(bs *handlerImpl) *broadcaster {
	b := &broadcaster{
		bs:    bs,
		queue: make(chan *cb.Envelope, bs.queueSize),
	}
	return b
}

func (b *broadcaster) drainQueue() {
	for {
		select {
		case msg, ok := <-b.queue:
			if ok {
				if !b.bs.target.Enqueue(msg) {
					return
				}
			} else {
				return
			}
		case <-b.bs.exitChan:
			return
		}
	}
}

func (b *broadcaster) queueEnvelopes(srv ab.AtomicBroadcast_BroadcastServer) error {

	for {
		msg, err := srv.Recv()
		if err != nil {
			return err
		}

		action, _ := b.bs.filters.Apply(msg)

		switch action {
		case broadcastfilter.Reconfigure:
			fallthrough
		case broadcastfilter.Accept:
			select {
			case b.queue <- msg:
				err = srv.Send(&ab.BroadcastResponse{Status: cb.Status_SUCCESS})
			default:
				err = srv.Send(&ab.BroadcastResponse{Status: cb.Status_SERVICE_UNAVAILABLE})
			}
		case broadcastfilter.Forward:
			fallthrough
		case broadcastfilter.Reject:
			err = srv.Send(&ab.BroadcastResponse{Status: cb.Status_BAD_REQUEST})
		default:
			logger.Fatalf("Unknown filter action :%v", action)
		}

		if err != nil {
			return err
		}
	}
}
