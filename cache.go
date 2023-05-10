package minicache

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

type cacheRules struct {
	ttl time.Duration
}

type HandlerFunc func(path []string) []byte

type route struct {
	handler        HandlerFunc
	staticChildren map[string]*route
	dynamicChild   *route
	cacheRules     cacheRules
}

type cacheEntry struct {
	value  []byte
	expiry time.Time
	sync.RWMutex
}

type cache struct {
	root       *route
	cache      map[string]*cacheEntry
	cacheRules cacheRules
	sync.RWMutex
}

func New() *cache {
	return &cache{root: newRoute(), cache: make(map[string]*cacheEntry)}
}

func newRoute() *route {
	r := &route{}
	r.staticChildren = make(map[string]*route)
	return r
}

func (r *route) getOrCreateChild(segment string) *route {
	if segment == "" {
		return r
	}
	if segment == "*" {
		if r.dynamicChild == nil {
			r.dynamicChild = newRoute()
		}
		return r.dynamicChild
	}
	if _, ok := r.staticChildren[segment]; !ok {
		r.staticChildren[segment] = newRoute()
	}
	return r.staticChildren[segment]
}

func (c *cache) Register(path string, handler HandlerFunc) error {
	r := c.root
	for _, p := range strings.Split(path, "/") {
		r = r.getOrCreateChild(p)
	}
	r.handler = handler
	return nil
}

func (c *cache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var path []string
	path = strings.Split(r.URL.EscapedPath(), "/")
	route := c.lookup(path)
	b := c.request(route, path)
	w.Write(b)
}

func (c *cache) request(r *route, p []string) []byte {
	c.Lock()
	if c.cache[toCanonicalPath(p)] == nil {
		entry := &cacheEntry{}
		c.cache[toCanonicalPath(p)] = entry
		c.Unlock()
		entry.Lock()
		entry.value = r.handler(p)
		entry.expiry = time.Now().Add(r.cacheRules.ttl)
		entry.Unlock()
	} else {
		c.Unlock()
	}
	c.RLock()
	entry := c.cache[toCanonicalPath(p)]
	c.RUnlock()
	entry.RLock()
	defer entry.RUnlock()
	if entry.expiry.Before(time.Now()) {
		go func() {
			entry.Lock()
			defer entry.Unlock()
			entry.value = r.handler(p)
		}()
	}
	return entry.value
}

func (c *cache) ListenAndServe(addr string) error {
	srv := http.Server{}
	srv.Addr = addr
	srv.Handler = c
	return srv.ListenAndServe()
}

func (c *cache) lookup(path []string) *route {
	r := c.root
	for _, p := range path {
		candidate := r.dynamicChild
		for child := range r.staticChildren {
			if p == child {
				candidate = r.staticChildren[child]
				break
			}
		}
		if candidate == nil {
			break
		}
		r = candidate
	}
	return r
}

func toCanonicalPath(p []string) string {
	out := ""
	for i := range p {
		if p[i] != "" {
			out += "/"
			out += p[i]
		}
	}
	return out
}
