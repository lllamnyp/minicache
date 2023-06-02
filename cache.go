package minicache

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

type cacheRules struct {
	ttl time.Duration
}

type HandlerFunc func(path []string) ([]byte, error)

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
	l          logr.Logger
	sync.RWMutex
}

type OptionFunc func(c *cache) error

func WithDefaultTTL(ttl time.Duration) OptionFunc {
	return func(c *cache) error {
		c.cacheRules.ttl = ttl
		return nil
	}
}

func WithLogger(l logr.Logger) OptionFunc {
	return func(c *cache) error {
		c.l = l
		return nil
	}
}

func New(options ...OptionFunc) *cache {
	c := &cache{}
	for _, o := range options {
		if err := o(c); err != nil {
			panic(err)
		}
	}
	c.root = newRoute()
	c.root.cacheRules = c.cacheRules
	c.cache = make(map[string]*cacheEntry)
	return c
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
			r.dynamicChild.cacheRules = r.cacheRules
		}
		return r.dynamicChild
	}
	if _, ok := r.staticChildren[segment]; !ok {
		r.staticChildren[segment] = newRoute()
		r.staticChildren[segment].cacheRules = r.cacheRules
	}
	return r.staticChildren[segment]
}

func (c *cache) Register(path string, handler HandlerFunc) error {
	segments, err := fromPath(path)
	if err != nil {
		return err
	}
	r := c.root
	for _, p := range segments {
		r = r.getOrCreateChild(p)
	}
	r.handler = handler
	return nil
}

func (c *cache) RegisterOrDie(path string, handler HandlerFunc) {
	if err := c.Register(path, handler); err != nil {
		panic(err)
	}
}

func (c *cache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, err := fromPath(r.URL.EscapedPath())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Add("Content-Type", "text/plain")
		_, err = w.Write([]byte(err.Error()))
		if err != nil {
			c.l.Error(err, "error writing response")
		}
		return
	}
	route := c.lookup(path)
	b, err := c.request(route, path)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Add("Content-Type", "text/plain")
		_, err = w.Write([]byte(err.Error()))
		if err != nil {
			c.l.Error(err, "error writing response")
		}
		return
	}
	w.Header().Add("Content-Type", "application/json")
	_, err = w.Write(b)
	if err != nil {
		c.l.Error(err, "error writing response")
	}
}

func (c *cache) request(r *route, p []string) ([]byte, error) {
	c.Lock()
	if c.cache[toCanonicalPath(p)] == nil {
		c.l.Info("cache miss", "key", p)
		entry := &cacheEntry{}
		c.cache[toCanonicalPath(p)] = entry
		c.Unlock()
		entry.Lock()
		var err error
		if entry.value, err = r.handler(p); err != nil {
			c.l.Error(err, "failed to populate cache", "key", p)
			entry.value = nil
			entry.Unlock()
			c.Lock()
			delete(c.cache, toCanonicalPath(p))
			c.Unlock()
			return nil, err
		}
		entry.expiry = time.Now().Add(r.cacheRules.ttl)
		c.l.Info("populated cache", "key", p, "expires-at", entry.expiry.Format(time.RFC3339))
		entry.Unlock()
	} else {
		c.Unlock()
		c.l.Info("cache hit", "key", p)
	}
	c.RLock()
	entry := c.cache[toCanonicalPath(p)]
	c.RUnlock()
	entry.RLock()
	rValue := entry.value
	defer entry.RUnlock()
	if rValue == nil {
		c.l.Info("empty cache value", "key", p)
		return nil, errors.New("cache entry found, but no value stored, try again later")
	}
	if entry.expiry.Before(time.Now()) {
		c.l.Info("stale cache entry, will renew", "key", p, "expires-at", entry.expiry.Format(time.RFC3339))
		go func() {
			entry.Lock()
			defer entry.Unlock()
			newBytes, err := r.handler(p)
			if err != nil {
				c.l.Error(err, "cache renewal failed", "key", p)
				return
			}
			entry.value = newBytes
			entry.expiry = time.Now().Add(r.cacheRules.ttl)
		}()
	}
	return rValue, nil
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
		if p == "" {
			continue
		}
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

func fromPath(p string) ([]string, error) {
	out := make([]string, 0, 8)
	for _, segment := range strings.Split(p, "/") {
		if segment == "" {
			continue
		}
		elem, err := url.PathUnescape(segment)
		if err != nil {
			return nil, err
		}
		out = append(out, elem)
	}
	return out, nil
}

func toCanonicalPath(p []string) string {
	sanitized := make([]string, 0, len(p))
	for i := range p {
		if p[i] == "" {
			continue
		}
		sanitized = append(sanitized, url.PathEscape(p[i]))
	}
	return "/" + strings.Join(sanitized, "/")
}
