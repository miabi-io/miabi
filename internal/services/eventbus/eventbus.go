// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package eventbus is an in-process pub/sub for SSE fan-out; single-node only, a separate worker would need Redis.
package eventbus

import "sync"

// Event is a single message on a topic.
type Event struct {
	Type string `json:"type"` // e.g. "log", "status"
	Data any    `json:"data"`
}

// Bus is a topic-based fan-out broker.
type Bus struct {
	mu   sync.RWMutex
	subs map[string]map[int]chan Event
	next int
}

func New() *Bus {
	return &Bus{subs: make(map[string]map[int]chan Event)}
}

// Subscribe returns a channel of events for a topic and an unsubscribe func.
func (b *Bus) Subscribe(topic string) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[topic] == nil {
		b.subs[topic] = make(map[int]chan Event)
	}
	id := b.next
	b.next++
	ch := make(chan Event, 64)
	b.subs[topic][id] = ch

	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if m, ok := b.subs[topic]; ok {
			if c, ok := m[id]; ok {
				close(c)
				delete(m, id)
			}
			if len(m) == 0 {
				delete(b.subs, topic)
			}
		}
	}
}

// Slow subscribers with a full buffer drop the event rather than block producers.
func (b *Bus) Publish(topic string, e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs[topic] {
		select {
		case ch <- e:
		default:
		}
	}
}
