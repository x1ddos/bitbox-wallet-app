// Copyright 2019 Shift Devices AG
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package rpcclient manages the connection with the bitboxbase, establishing a websocket listener and
// sending events when receiving packets. It also acts as a rpc client for any external package wanting
// to communicate with the base
package rpcclient

import (
	"fmt"
	"net/http"
	"net/rpc"

	"github.com/digitalbitbox/bitbox-wallet-app/util/errp"
	"github.com/digitalbitbox/bitbox-wallet-app/util/logging"
	"github.com/digitalbitbox/bitbox-wallet-app/util/observable"
	"github.com/digitalbitbox/bitbox-wallet-app/util/observable/action"
	"github.com/flynn/noise"
	"github.com/gorilla/websocket"

	"github.com/sirupsen/logrus"
)

const (
	opRPCCall     = byte('r')
	opUCanHasDemo = byte('d')
)

type rpcConn struct {
	readChan  chan []byte
	writeChan chan []byte
	closeChan chan struct{}
}

// newRPCConn returns a pointer to a rpcConn struct. RPCConn is used as an io.ReadWriteCloser by the rpc connection.
func newRPCConn() *rpcConn {
	RPCConn := &rpcConn{
		readChan:  make(chan []byte),
		writeChan: make(chan []byte),
		closeChan: make(chan struct{}),
	}
	return RPCConn
}

func (conn *rpcConn) ReadChan() chan []byte {
	return conn.readChan
}

func (conn *rpcConn) WriteChan() chan []byte {
	return conn.writeChan
}

func (conn *rpcConn) CloseChan() chan struct{} {
	return conn.closeChan
}

func (conn *rpcConn) Read(p []byte) (n int, err error) {
	message := <-conn.readChan
	return copy(p, message), nil
}

func (conn *rpcConn) Write(p []byte) (n int, err error) {
	conn.writeChan <- p
	return len(p), nil
}

func (conn *rpcConn) Close() error {
	if conn.closeChan != nil {
		close(conn.closeChan)
		conn.closeChan = nil
	}
	return nil
}

// GetEnvResponse holds the information from the rpc call reply to get some environment data from the base
type GetEnvResponse struct {
	Network        string
	ElectrsRPCPort string
}

// SampleInfoResponse holds some sample information from the BitBox Base
type SampleInfoResponse struct {
	Blocks         int64   `json:"blocks"`
	Difficulty     float64 `json:"difficulty"`
	LightningAlias string  `json:"lightningAlias"`
}

// RPCClient implements observable blockchainInfo.
type RPCClient struct {
	observable.Implementation
	sampleInfo          *SampleInfoResponse
	log                 *logrus.Entry
	address             string
	bitboxBaseConfigDir string

	bitboxBaseNoiseStaticPubkey   []byte
	channelHash                   string
	channelHashAppVerified        bool
	channelHashBitBoxBaseVerified bool
	sendCipher, receiveCipher     *noise.CipherState

	onUnregister func(string)
	bitboxBaseID string

	//rpc stuff
	client        *rpc.Client
	rpcConnection *rpcConn
}

// NewRPCClient returns a new bitboxbase rpcClient.
func NewRPCClient(address string, bitboxBaseConfigDir string, onUnregister func(string), bitboxBaseID string) *RPCClient {
	rpcClient := &RPCClient{
		bitboxBaseID:        bitboxBaseID,
		log:                 logging.Get().WithGroup("bitboxbase"),
		address:             address,
		sampleInfo:          &SampleInfoResponse{},
		bitboxBaseConfigDir: bitboxBaseConfigDir,
		rpcConnection:       newRPCConn(),
		onUnregister:        onUnregister,
	}
	return rpcClient
}

// Ping sends a get request to the bitbox base's middleware root handler and returns true if successful
func (rpcClient *RPCClient) Ping() (bool, error) {
	response, err := http.Get("http://" + rpcClient.address + "/")
	if err != nil {
		rpcClient.log.Println("No response from middleware", err)
		return false, err
	}

	if response.StatusCode != http.StatusOK {
		rpcClient.log.Println("Received http status code from middleware other than 200")
		return false, nil
	}
	return true, nil
}

// Connect starts the websocket go routine, first checking if the middleware is reachable,
// then establishing a websocket connection, then authenticating and encrypting all further traffic with noise.
func (rpcClient *RPCClient) Connect(bitboxBaseID string) error {
	rpcClient.bitboxBaseID = bitboxBaseID
	rpcClient.log.Printf("connecting to base websocket")
	connected, err := rpcClient.Ping()
	if err != nil {
		return err
	}
	if !connected {
		return errp.New("rpcClient: Failed to connect with the bitboxbase middleware")
	}
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+rpcClient.address+"/ws", nil)
	if err != nil {
		return errp.New("rpcClient: failed to create new websocket client")
	}
	if err = rpcClient.initializeNoise(ws, bitboxBaseID); err != nil {
		return err
	}
	rpcClient.client = rpc.NewClient(rpcClient.rpcConnection)
	rpcClient.runWebsocket(ws, rpcClient.rpcConnection.WriteChan())
	return nil
}

func (rpcClient *RPCClient) parseMessage(message []byte) {
	if len(message) == 0 {
		rpcClient.log.Println("Received empty message")
		return
	}
	opCode := message[0]
	switch opCode {
	case opUCanHasDemo:
		go rpcClient.SampleInfo()
	case opRPCCall:
		message := message[1:]
		rpcClient.rpcConnection.ReadChan() <- message
	default:
		rpcClient.log.Println("Received message without opCode")
	}
}

// Stop shuts down the websocket connection with the base
func (rpcClient *RPCClient) Stop() {
	rpcClient.client.Close()
}

// GetEnv makes a synchronous rpc call to the base and returns the network type and electrs rpc port
func (rpcClient *RPCClient) GetEnv() (string, string) {
	var reply GetEnvResponse
	request := 1
	err := rpcClient.client.Call("RPCServer.GetSystemEnv", request, &reply)
	if err != nil {
		rpcClient.log.Printf("Failed with: %v", err)
	}
	return reply.Network, reply.ElectrsRPCPort
}

// SampleInfo make a synchronous rpc call to the base, emits an event containing the SampleInfo struct
// to the frontend and returns the SampleInfo struct
func (rpcClient *RPCClient) SampleInfo() SampleInfoResponse {
	var reply SampleInfoResponse
	request := 1
	err := rpcClient.client.Call("RPCServer.GetSampleInfo", request, &reply)
	if err != nil {
		rpcClient.log.Printf("Failed with: %v", err)
	}
	rpcClient.Notify(observable.Event{
		Subject: fmt.Sprintf("/bitboxbases/%s/middlewareinfo", rpcClient.bitboxBaseID),
		Action:  action.Replace,
		Object:  reply,
	})

	return reply
}
