// Package lru provides a simple LRU cache for Helix Cluster OS.
package lru

import "container/list"

// Cache is an LRU cache.
type Cache struct {
	cap   int
	items map[string]*list.Element
	order *list.List
}

type entry struct {
	key   string
	value interface{}
}

// NewCache creates a new Cache with the given capacity.
func NewCache(capacity int) *Cache {
	return &Cache{
		cap:   capacity,
		items: make(map[string]*list.Element),
		order: list.New(),
	}
}

// Get retrieves a value.
func (c *Cache) Get(key string) (interface{}, bool) {
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*entry).value, true
	}
	return nil, false
}

// Put inserts or updates a value.
func (c *Cache) Put(key string, value interface{}) {
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		el.Value.(*entry).value = value
		return
	}
	if c.order.Len() >= c.cap {
		back := c.order.Back()
		if back != nil {
			c.order.Remove(back)
			delete(c.items, back.Value.(*entry).key)
		}
	}
	el := c.order.PushFront(&entry{key: key, value: value})
	c.items[key] = el
}
