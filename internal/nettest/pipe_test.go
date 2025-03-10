// The MIT License
//
// Copyright (c) 2020 Temporal Technologies Inc.  All rights reserved.
//
// Copyright (c) 2020 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package nettest

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPipe_Accept(t *testing.T) {
	t.Parallel()

	listener := NewPipe()

	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(1)

	go func() {
		defer wg.Done()

		c, err := listener.Accept(nil)
		assert.NoError(t, err)

		defer func() {
			assert.NoError(t, c.Close())
		}()
	}()

	c, err := listener.Connect(nil)
	assert.NoError(t, err)

	defer func() {
		assert.NoError(t, c.Close())
	}()
}

func TestPipe_ClientCanceled(t *testing.T) {
	t.Parallel()

	listener := NewPipe()
	done := make(chan struct{})
	close(done) // hi efe
	_, err := listener.Connect(done)
	assert.ErrorIs(t, err, ErrCanceled)
}

func TestPipe_ServerCanceled(t *testing.T) {
	t.Parallel()

	listener := NewPipe()
	done := make(chan struct{})
	close(done)
	_, err := listener.Accept(done)
	assert.ErrorIs(t, err, ErrCanceled)
}
