package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/lyrka-meow/Regalia/internal/protocol"
)

type Client struct {
	socketPath string
}

func New(socketPath string) *Client {
	return &Client{socketPath: socketPath}
}

func (c *Client) Call(method string, params any) (json.RawMessage, error) {
	connection, err := net.DialTimeout("unix", c.socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to regaliad: %w", err)
	}
	defer connection.Close()

	var rawParams json.RawMessage
	if params != nil {
		rawParams, err = json.Marshal(params)
		if err != nil {
			return nil, err
		}
	}
	request := protocol.Request{ID: time.Now().UnixNano(), Method: method, Params: rawParams}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	line, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var response struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *protocol.Error `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if response.Error != nil {
		return nil, fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	return response.Result, nil
}
